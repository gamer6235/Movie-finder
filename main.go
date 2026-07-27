package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"google.golang.org/genai"
)

func main() {
	// 1. Render Port Scanner പ്രശ്നം ഒഴിവാക്കാനുള്ള Dummy HTTP Server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "Bot is active!")
		})
		log.Printf("Dummy Web Server running on port %s", port)
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	// 2. Fetch Environment Variables
	telegramToken := os.Getenv("TELEGRAM_TOKEN")
	if telegramToken == "" || telegramToken == "YOUR_TELEGRAM_BOT_TOKEN" {
		log.Fatal("❌ ERROR: TELEGRAM_TOKEN environment variable is missing or invalid!")
	}

	geminiAPIKey := os.Getenv("GEMINI_API_KEY")
	if geminiAPIKey == "" || geminiAPIKey == "YOUR_GEMINI_API_KEY" {
		log.Fatal("❌ ERROR: GEMINI_API_KEY environment variable is missing or invalid!")
	}

	ctx := context.Background()

	// 3. Initialize Gemini Client
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: geminiAPIKey,
	})
	if err != nil {
		log.Fatalf("Gemini Client initialization error: %v", err)
	}

	// 4. Initialize Telegram Bot
	bot, err := tgbotapi.NewBotAPI(telegramToken)
	if err != nil {
		log.Fatalf("Telegram Bot API initialization failed: %v. Check TELEGRAM_TOKEN!", err)
	}

	log.Printf("🚀 Go Bot Successfully Started as @%s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	// Update handling loop
	for update := range updates {
		if update.Message == nil {
			continue
		}

		// Photos handle ചെയ്യാൻ
		if len(update.Message.Photo) > 0 {
			go handleMedia(ctx, bot, client, update.Message, "photo")
		}

		// Short Video Clips handle ചെയ്യാൻ
		if update.Message.Video != nil {
			go handleMedia(ctx, bot, client, update.Message, "video")
		}
	}
}

func handleMedia(ctx context.Context, bot *tgbotapi.BotAPI, client *genai.Client, msg *tgbotapi.Message, mediaType string) {
	chatID := msg.Chat.ID

	statusMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "🔎 Media analyze cheyyunnu... Please wait!"))

	var fileID string
	var mimeType string

	if mediaType == "photo" {
		photos := msg.Photo
		fileID = photos[len(photos)-1].FileID
		mimeType = "image/jpeg"
	} else if mediaType == "video" {
		// 20MB Max size limit check
		if msg.Video.FileSize > 20*1024*1024 {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Video size is too large! Please send clips under 20MB."))
			return
		}
		fileID = msg.Video.FileID
		mimeType = "video/mp4"
	}

	// Telegram Server-ൽ നിന്ന് Direct Link എടുക്കുന്നു
	fileURL, err := bot.GetFileDirectURL(fileID)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Download URL ലഭിച്ചില്ല!"))
		return
	}

	// Media ഡൗൺലോഡ് ചെയ്യുന്നു
	resp, err := http.Get(fileURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Media download ചെയ്യാൻ കഴിഞ്ഞില്ല!"))
		return
	}
	defer resp.Body.Close()

	mediaBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to read media file!"))
		return
	}

	// Gemini Prompt
	prompt := `Analyze this movie/series scene carefully. Identify which movie or TV/web series this scene belongs to. 
Provide response in this exact format:
🎬 **Title:** [Movie/Series Name]
📅 **Release Year:** [Year]
🎭 **Main Actors in scene:** [Names if visible]
📝 **Short Summary:** [1-2 sentences]`

	// Gemini 2.5 Flash API Call
	genResp, err := client.Models.GenerateContent(ctx, "gemini-3.6-flash", []*genai.Content{
		{
			Parts: []*genai.Part{
				{
					InlineData: &genai.Blob{
						MIMEType: mimeType,
						Data:     mediaBytes,
					},
				},
				{
					Text: prompt,
				},
			},
		},
	}, nil)

	// Status message ഡിലീറ്റ് ചെയ്യൽ
	deleteMsg := tgbotapi.NewDeleteMessage(chatID, statusMsg.MessageID)
	bot.Request(deleteMsg)

	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Gemini Error: %v", err)))
		return
	}

	// ഫലം ടെലിഗ്രാമിൽ അയക്കുന്നു
	if len(genResp.Candidates) > 0 && genResp.Candidates[0].Content != nil {
		replyText := genResp.Candidates[0].Content.Parts[0].Text
		reply := tgbotapi.NewMessage(chatID, replyText)
		reply.ParseMode = "Markdown"
		bot.Send(reply)
	} else {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Movie/Series കണ്ടെത്താൻ കഴിഞ്ഞില്ല."))
	}
}

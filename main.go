package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"google.golang.org/genai"
)

func main() {
	// 1. Render Port Scanner സപ്പോർട്ടിനായുള്ള Dummy HTTP Server
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

	// Allowed Group ID എടുക്കുന്നു
	allowedGroupIDStr := os.Getenv("ALLOWED_GROUP_ID")
	var allowedGroupID int64 = 0
	if allowedGroupIDStr != "" {
		var parseErr error
		allowedGroupID, parseErr = strconv.ParseInt(allowedGroupIDStr, 10, 64)
		if parseErr != nil {
			log.Fatalf("❌ ERROR: Invalid ALLOWED_GROUP_ID format! Must be an integer like -100xxxxxxxxxx")
		}
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
		msg := update.Message
		if msg == nil {
			msg = update.ChannelPost
		}

		if msg == nil {
			continue
		}

		// ഗ്രൂപ്പ് ഐഡി ഫിൽട്ടർ ചെയ്യൽ (സെറ്റ് ചെയ്തിട്ടുണ്ടെങ്കിൽ മാത്രം)
		if allowedGroupID != 0 && msg.Chat.ID != allowedGroupID {
			continue
		}

		// സ്വന്തം ബോട്ട് അയക്കുന്ന മെസ്സേജുകൾ ഒഴിവാക്കാൻ
		if msg.From != nil && msg.From.ID == bot.Self.ID {
			continue
		}

		// Photos handle ചെയ്യാൻ
		if len(msg.Photo) > 0 {
			go handleMedia(ctx, bot, client, msg, "photo")
			continue
		}

		// Videos handle ചെയ്യാൻ
		if msg.Video != nil {
			go handleMedia(ctx, bot, client, msg, "video")
			continue
		}

		// Downloader Bot അയക്കുന്ന Document type Video-കൾ handle ചെയ്യാൻ
		if msg.Document != nil {
			go handleMedia(ctx, bot, client, msg, "document_video")
			continue
		}
	}
}

func handleMedia(ctx context.Context, bot *tgbotapi.BotAPI, client *genai.Client, msg *tgbotapi.Message, mediaType string) {
	chatID := msg.Chat.ID

	var fileID string
	var mimeType string

	if mediaType == "photo" {
		photos := msg.Photo
		fileID = photos[len(photos)-1].FileID
		mimeType = "image/jpeg"
	} else if mediaType == "video" || mediaType == "document_video" {
		var fileSize int
		if mediaType == "video" {
			fileID = msg.Video.FileID
			fileSize = msg.Video.FileSize
		} else {
			// Check if document is actually video/image
			if msg.Document.MimeType != "video/mp4" && msg.Document.MimeType != "video/mkv" && msg.Document.MimeType != "image/jpeg" && msg.Document.MimeType != "image/png" {
				return
			}
			fileID = msg.Document.FileID
			fileSize = msg.Document.FileSize
		}

		// 20MB Max size limit check
		if fileSize > 20*1024*1024 {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Video size is too large! Please send clips under 20MB."))
			return
		}
		mimeType = "video/mp4"
	}

	statusMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "🔎 Media analyze cheyyunnu... Please wait!"))

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

	// Gemini 2.0 Flash API Call
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
	if statusMsg.MessageID != 0 {
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, statusMsg.MessageID)
		bot.Request(deleteMsg)
	}

	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Gemini Error: %v", err)))
		return
	}

	// ഫലം ടെലിഗ്രാമിൽ അയക്കുന്നു
	if len(genResp.Candidates) > 0 && genResp.Candidates[0].Content != nil {
		replyText := genResp.Candidates[0].Content.Parts[0].Text
		reply := tgbotapi.NewMessage(chatID, replyText)
		reply.ReplyToMessageID = msg.MessageID
		reply.ParseMode = "Markdown"
		bot.Send(reply)
	} else {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Movie/Series കണ്ടെത്താൻ കഴിഞ്ഞില്ല."))
	}
}

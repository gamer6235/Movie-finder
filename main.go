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
	// 1. Dummy HTTP Server for Render
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
	if telegramToken == "" {
		log.Fatal("❌ ERROR: TELEGRAM_TOKEN missing!")
	}

	geminiAPIKey := os.Getenv("GEMINI_API_KEY")
	if geminiAPIKey == "" {
		log.Fatal("❌ ERROR: GEMINI_API_KEY missing!")
	}

	allowedGroupIDStr := os.Getenv("ALLOWED_GROUP_ID")
	var allowedGroupID int64 = 0
	if allowedGroupIDStr != "" {
		allowedGroupID, _ = strconv.ParseInt(allowedGroupIDStr, 10, 64)
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
		log.Fatalf("Telegram Bot API error: %v", err)
	}

	log.Printf("🚀 Go Bot Started as @%s", bot.Self.UserName)

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

		// Check Allowed Group
		if allowedGroupID != 0 && msg.Chat.ID != allowedGroupID {
			continue
		}

		// Avoid self loops
		if msg.From != nil && msg.From.ID == bot.Self.ID {
			continue
		}

		// Case A: ഉപയോക്താവ് നേരിട്ട് അയക്കുന്ന Media / Photos / Videos
		if processMediaMessage(ctx, bot, client, msg) {
			continue
		}

		// Case B: YT Bot അയച്ച വീഡിയോയിലേക്ക് ഉപയോക്താവ് REPLY ചെയ്യുമ്പോൾ
		if msg.ReplyToMessage != nil {
			processMediaMessage(ctx, bot, client, msg.ReplyToMessage)
		}
	}
}

func processMediaMessage(ctx context.Context, bot *tgbotapi.BotAPI, client *genai.Client, msg *tgbotapi.Message) bool {
	// Direct Photos
	if len(msg.Photo) > 0 {
		go handleMedia(ctx, bot, client, msg, "photo")
		return true
	}

	// Direct Videos
	if msg.Video != nil {
		go handleMedia(ctx, bot, client, msg, "video")
		return true
	}

	// Document Videos
	if msg.Document != nil {
		go handleMedia(ctx, bot, client, msg, "document")
		return true
	}

	return false
}

func handleMedia(ctx context.Context, bot *tgbotapi.BotAPI, client *genai.Client, msg *tgbotapi.Message, mediaType string) {
	chatID := msg.Chat.ID

	var fileID string
	var mimeType string = "video/mp4"

	if mediaType == "photo" {
		photos := msg.Photo
		fileID = photos[len(photos)-1].FileID
		mimeType = "image/jpeg"
	} else if mediaType == "video" {
		if msg.Video.FileSize > 20*1024*1024 {
			return
		}
		fileID = msg.Video.FileID
	} else if mediaType == "document" {
		if msg.Document.FileSize > 20*1024*1024 {
			return
		}
		fileID = msg.Document.FileID
		if msg.Document.MimeType != "" {
			mimeType = msg.Document.MimeType
		}
	}

	statusMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "🔎 Media analyze cheyyunnu... Please wait!"))

	fileURL, err := bot.GetFileDirectURL(fileID)
	if err != nil {
		if statusMsg.MessageID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(chatID, statusMsg.MessageID))
		}
		return
	}

	resp, err := http.Get(fileURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		if statusMsg.MessageID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(chatID, statusMsg.MessageID))
		}
		return
	}
	defer resp.Body.Close()

	mediaBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		if statusMsg.MessageID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(chatID, statusMsg.MessageID))
		}
		return
	}

	prompt := `Analyze this movie/series scene carefully. Identify which movie or TV/web series this scene belongs to. 
Provide response in this exact format:
🎬 **Title:** [Movie/Series Name]
📅 **Release Year:** [Year]
🎭 **Main Actors in scene:** [Names if visible]
📝 **Short Summary:** [1-2 sentences]`

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

	if statusMsg.MessageID != 0 {
		bot.Request(tgbotapi.NewDeleteMessage(chatID, statusMsg.MessageID))
	}

	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Gemini Error: %v", err)))
		return
	}

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

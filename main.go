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
	// 1. Render Port Scanner-ne fool cheyyan oru dummy HTTP Server
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
		if update.Message == nil || len(update.Message.Photo) == 0 {
			continue
		}

		go handlePhoto(ctx, bot, client, update.Message)
	}
}

func handlePhoto(ctx context.Context, bot *tgbotapi.BotAPI, client *genai.Client, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	// Send processing status message
	statusMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "🔎 Scene analyze cheyyunnu... Please wait!"))

	// Get highest resolution photo
	photos := msg.Photo
	largestPhoto := photos[len(photos)-1]

	fileURL, err := bot.GetFileDirectURL(largestPhoto.FileID)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Image download URL error!"))
		return
	}

	// Download image bytes
	resp, err := http.Get(fileURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Image download failed!"))
		return
	}
	defer resp.Body.Close()

	imgBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to read image data!"))
		return
	}

	// Gemini Prompt
	prompt := `Analyze this scene carefully. Identify which movie or TV/web series this scene belongs to. 
Provide response in this exact format:
🎬 **Title:** [Movie/Series Name]
📅 **Release Year:** [Year]
🎭 **Main Actors in scene:** [Names if visible]
📝 **Short Summary:** [1-2 sentences]`

	// Gemini 2.5 Flash API Call
	genResp, err := client.Models.GenerateContent(ctx, "gemini-2.5-flash", []*genai.Content{
		{
			Parts: []*genai.Part{
				{
					InlineData: &genai.Blob{
						MIMEType: "image/jpeg",
						Data:     imgBytes,
					},
				},
				{
					Text: prompt,
				},
			},
		},
	}, nil)

	// Delete status message
	deleteMsg := tgbotapi.NewDeleteMessage(chatID, statusMsg.MessageID)
	bot.Request(deleteMsg)

	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Gemini Error: %v", err)))
		return
	}

	// Send Gemini result back
	if len(genResp.Candidates) > 0 && genResp.Candidates[0].Content != nil {
		replyText := genResp.Candidates[0].Content.Parts[0].Text
		reply := tgbotapi.NewMessage(chatID, replyText)
		reply.ParseMode = "Markdown"
		bot.Send(reply)
	} else {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Could not identify the movie/series."))
	}
}

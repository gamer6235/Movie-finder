package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"google.golang.org/genai"
)

var keyIndex uint64

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

	// Multiple API Keys split ചെയ്യുന്നു
	rawKeys := os.Getenv("GEMINI_API_KEYS")
	if rawKeys == "" {
		// പഴയ ഒരൊറ്റ Key മാച്ചിംഗിനായി fallback
		rawKeys = os.Getenv("GEMINI_API_KEY")
	}

	if rawKeys == "" {
		log.Fatal("❌ ERROR: GEMINI_API_KEYS environment variable is missing!")
	}

	apiKeys := strings.Split(rawKeys, ",")
	for i := range apiKeys {
		apiKeys[i] = strings.TrimSpace(apiKeys[i])
	}

	log.Printf("🔑 Loaded %d Gemini API Key(s) for rotation.", len(apiKeys))

	allowedGroupIDStr := os.Getenv("ALLOWED_GROUP_ID")
	var allowedGroupID int64 = 0
	if allowedGroupIDStr != "" {
		allowedGroupID, _ = strconv.ParseInt(allowedGroupIDStr, 10, 64)
	}

	ctx := context.Background()

	// 3. Initialize Telegram Bot
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
		if processMediaMessage(ctx, bot, apiKeys, msg) {
			continue
		}

		// Case B: YT Bot അയച്ച വീഡിയോയിലേക്ക് ഉപയോക്താവ് REPLY ചെയ്യുമ്പോൾ
		if msg.ReplyToMessage != nil {
			processMediaMessage(ctx, bot, apiKeys, msg.ReplyToMessage)
		}
	}
}

func processMediaMessage(ctx context.Context, bot *tgbotapi.BotAPI, apiKeys []string, msg *tgbotapi.Message) bool {
	if len(msg.Photo) > 0 {
		go handleMedia(ctx, bot, apiKeys, msg, "photo")
		return true
	}

	if msg.Video != nil {
		go handleMedia(ctx, bot, apiKeys, msg, "video")
		return true
	}

	if msg.Document != nil {
		go handleMedia(ctx, bot, apiKeys, msg, "document")
		return true
	}

	return false
}

func handleMedia(ctx context.Context, bot *tgbotapi.BotAPI, apiKeys []string, msg *tgbotapi.Message, mediaType string) {
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

	// Key Rotation Logic (വീണ്ടും ട്രൈ ചെയ്യും)
	var genResp *genai.GenerateContentResponse
	var genErr error

	totalKeys := len(apiKeys)
	startIndex := atomic.AddUint64(&keyIndex, 1)

	for i := 0; i < totalKeys; i++ {
		currentKey := apiKeys[(uint64(i)+startIndex)%uint64(totalKeys)]

		client, err := genai.NewClient(ctx, &genai.ClientConfig{
			APIKey: currentKey,
		})
		if err != nil {
			continue
		}

		genResp, genErr = client.Models.GenerateContent(ctx, "gemini-2.0-flash", []*genai.Content{
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

		// സക്സസ് ആയാൽ ലൂപ്പ് നിർത്തും
		if genErr == nil {
			break
		}
		log.Printf("⚠️ Key failed/rate limited. Trying next key... Error: %v", genErr)
	}

	if statusMsg.MessageID != 0 {
		bot.Request(tgbotapi.NewDeleteMessage(chatID, statusMsg.MessageID))
	}

	if genErr != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ All API Keys are currently rate-limited. Please try again in a minute!"))
		return
	}

	if genResp != nil && len(genResp.Candidates) > 0 && genResp.Candidates[0].Content != nil {
		replyText := genResp.Candidates[0].Content.Parts[0].Text
		reply := tgbotapi.NewMessage(chatID, replyText)
		reply.ReplyToMessageID = msg.MessageID
		reply.ParseMode = "Markdown"
		bot.Send(reply)
	} else {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Movie/Series കണ്ടെത്താൻ കഴിഞ്ഞില്ല."))
	}
}

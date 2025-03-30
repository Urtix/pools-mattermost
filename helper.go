package main

import (
	"encoding/json"
	"fmt"
	"github.com/mattermost/mattermost-server/v6/model"
	"os"
	"os/signal"
	"strings"
	"unicode"
)

func setupGracefulShutdown(app *application) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for range c {
			if app.mattermostWebSocketClient != nil {
				app.logger.Info().Msg("Closing websocket connection")
				app.mattermostWebSocketClient.Close()
			}
			app.logger.Info().Msg("Shutdown")
			os.Exit(0)
		}
	}()
}

func listenToEvents(app *application) {
	var err error
	failCount := 0
	for {
		app.mattermostWebSocketClient, err = model.NewWebSocketClient4(
			fmt.Sprintf("ws://%s", app.config.mattermostServer.Host+app.config.mattermostServer.Path),
			app.mattermostClient.AuthToken,
		)
		if err != nil {
			app.logger.Warn().Err(err).Msg("Mattermost websocket disconnected, retrying")
			failCount += 1
			continue
		}
		app.logger.Info().Msg("Mattermost connected to websocket")

		app.mattermostWebSocketClient.Listen()

		for event := range app.mattermostWebSocketClient.EventChannel {
			go handleWebSocketEvent(app, event)
		}
	}
}

func handleWebSocketEvent(app *application, event *model.WebSocketEvent) {

	if event.EventType() != model.WebsocketEventPosted {
		return
	}

	post := &model.Post{}
	err := json.Unmarshal([]byte(event.GetData()["post"].(string)), &post)
	if err != nil {
		app.logger.Error().Err(err).Msg("Failed to convert event to *model.Post")
	}

	if post.UserId == app.mattermostUser.Id {
		return
	}

	handlePost(app, post)
}

func sendResponse(app *application, channelId string, msg string, originalPost *model.Post) {
	post := &model.Post{
		ChannelId: channelId,
		Message:   msg,
		RootId:    originalPost.RootId,
	}

	if originalPost.RootId == "" {
		post.RootId = originalPost.Id
	}

	if _, _, err := app.mattermostClient.CreatePost(post); err != nil {
		app.logger.Error().Err(err).Str("channel", channelId).Msg("Failed to create post")
	}
}

func sendHelp(app *application, channelId string, originalPost *model.Post) {
	helpText := `Некорректный формат команды. Пример использования:
/poll 2h "Лучший язык?" "Go" "Python"
`
	sendResponse(app, channelId, helpText, originalPost)
}

func splitArgs(input string) ([]string, error) {
	var args []string
	current := ""
	inQuotes := false
	input = strings.TrimSpace(input)

	for i, r := range input {
		switch {
		case r == '"':
			current += string(r)
			if i > 0 && input[i-1] == '\\' {
				current = current[:len(current)-2] + `"`
			} else {
				inQuotes = !inQuotes
			}
		case unicode.IsSpace(r) && !inQuotes:
			if current != "" {
				args = append(args, current)
				current = ""
			}
		default:
			current += string(r)
		}
	}

	if current != "" {
		args = append(args, current)
	}

	if inQuotes {
		return nil, fmt.Errorf("Незакрытые кавычки")
	}

	return args, nil
}

package main

import (
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "github.com/joho/godotenv/autoload"
	"github.com/mattermost/mattermost-server/v6/model"
	"github.com/rs/zerolog"
)

// 1. Обновляем структуру Poll
type Poll struct {
	ID        string
	Question  string
	Options   []string
	Votes     map[int]int
	Voters    map[string]struct{}
	CreatorID string
	ChannelID string
	CreatedAt time.Time
	EndTime   time.Time // Добавляем время окончания
}

type PollStorage struct {
	sync.RWMutex
	Polls map[string]Poll
}

type config struct {
	mattermostUserName string
	mattermostTeamName string
	mattermostToken    string
	mattermostServer   *url.URL
}

func loadConfig() config {
	var settings config

	settings.mattermostTeamName = os.Getenv("MM_TEAM")
	settings.mattermostUserName = os.Getenv("MM_USERNAME")
	settings.mattermostToken = os.Getenv("MM_TOKEN")
	settings.mattermostServer, _ = url.Parse(os.Getenv("MM_SERVER"))

	return settings
}

// application struct to hold the dependencies for our bot
type application struct {
	config                    config
	logger                    zerolog.Logger
	mattermostClient          *model.Client4
	mattermostWebSocketClient *model.WebSocketClient
	mattermostUser            *model.User
	mattermostTeam            *model.Team
	pollStorage               *PollStorage // Добавляем хранилище
}

func main() {

	app := &application{
		pollStorage: &PollStorage{
			Polls: make(map[string]Poll),
		},
		logger: zerolog.New(
			zerolog.ConsoleWriter{
				Out:        os.Stdout,
				TimeFormat: time.RFC822,
			},
		).With().Timestamp().Logger(),
	}

	app.config = loadConfig()
	app.logger.Info().Str("config", fmt.Sprint(app.config)).Msg("")

	setupGracefulShutdown(app)

	// Create a new mattermost client.
	app.mattermostClient = model.NewAPIv4Client(app.config.mattermostServer.String())

	// Login.
	app.mattermostClient.SetToken(app.config.mattermostToken)

	if user, resp, err := app.mattermostClient.GetUser("me", ""); err != nil {
		app.logger.Fatal().Err(err).Msg("Could not log in")
	} else {
		app.logger.Debug().Interface("user", user).Interface("resp", resp).Msg("")
		app.logger.Info().Msg("Logged in to mattermost")
		app.mattermostUser = user
	}

	// Find and save the bot's team to app struct.
	if team, resp, err := app.mattermostClient.GetTeamByName(app.config.mattermostTeamName, ""); err != nil {
		app.logger.Fatal().Err(err).Msg("Could not find team. Is this bot a member ?")
	} else {
		app.logger.Debug().Interface("team", team).Interface("resp", resp).Msg("")
		app.mattermostTeam = team
	}

	// Listen to live events coming in via websocket.
	listenToEvents(app)
}

func setupGracefulShutdown(app *application) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for range c {
			if app.mattermostWebSocketClient != nil {
				app.logger.Info().Msg("Closing websocket connection")
				app.mattermostWebSocketClient.Close()
			}
			app.logger.Info().Msg("Shutting down")
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
			// TODO: backoff based on failCount and sleep for a while.
			continue
		}
		app.logger.Info().Msg("Mattermost websocket connected")

		app.mattermostWebSocketClient.Listen()

		for event := range app.mattermostWebSocketClient.EventChannel {
			// Launch new goroutine for handling the actual event.
			// If required, you can limit the number of events beng processed at a time.
			go handleWebSocketEvent(app, event)
		}
	}
}

func sendResponse(app *application, channelId string, msg string, originalPost *model.Post) {
	post := &model.Post{
		ChannelId: channelId,
		Message:   msg,
		RootId:    originalPost.RootId, // Берём RootId из оригинального сообщения
	}

	// Если оригинальное сообщение - корень треда, используем его ID как RootId
	if originalPost.RootId == "" {
		post.RootId = originalPost.Id
	}

	if _, _, err := app.mattermostClient.CreatePost(post); err != nil {
		app.logger.Error().Err(err).Str("channel", channelId).Msg("Failed to create post")
	}
}

func handleWebSocketEvent(app *application, event *model.WebSocketEvent) {

	// Ignore other types of events.
	if event.EventType() != model.WebsocketEventPosted {
		return
	}

	// Since this event is a post, unmarshal it to (*model.Post)
	post := &model.Post{}
	err := json.Unmarshal([]byte(event.GetData()["post"].(string)), &post)
	if err != nil {
		app.logger.Error().Err(err).Msg("Could not cast event to *model.Post")
	}

	// Ignore messages sent by this bot itself.
	if post.UserId == app.mattermostUser.Id {
		return
	}

	// Handle however you want.
	handlePost(app, post)
}

func handlePost(app *application, post *model.Post) {
	app.logger.Debug().Str("message", post.Message).Msg("")
	app.logger.Debug().Interface("post", post).Msg("")

	// Проверяем команду /poll в начале сообщения
	if pollRegex := regexp.MustCompile(`(?m)^/poll\b`); pollRegex.MatchString(post.Message) {
		// Извлекаем аргументы с учетом кавычек
		args, err := splitArgs(post.Message)
		if err != nil {
			sendResponse(app, post.ChannelId, "Ошибка: Незакрытые кавычки", post)
			return
		}

		// Парсим длительность
		duration, err := time.ParseDuration(args[1])
		if err != nil {
			sendResponse(app, post.ChannelId,
				"Некорректный формат времени. Пример: 30m, 2h, 24h", post)
			return
		}

		// Проверяем наличие кавычек для каждого аргумента
		for _, arg := range args[2:] { // Пропускаем саму команду /poll
			if !strings.HasPrefix(arg, `"`) || !strings.HasSuffix(arg, `"`) {
				fmt.Println(arg)
				sendHelp(app, post.ChannelId, post)
				return
			}
		}

		// Проверяем минимальное количество аргументов (команда + вопрос + минимум 2 варианта)
		if len(args) < 5 {
			sendHelp(app, post.ChannelId, post)
			return
		}

		// Парсим аргументы
		question := strings.Trim(args[2], `"`)
		options := make([]string, 0, len(args)-3)
		for _, arg := range args[3:] {
			options = append(options, strings.Trim(arg, `"`))
		}

		// Генерируем UUID для опроса
		pollID := uuid.New().String()

		// Создаем структуру опроса
		newPoll := Poll{
			ID:        pollID,
			Question:  question,
			Options:   options,
			CreatorID: post.UserId,
			ChannelID: post.ChannelId,
			CreatedAt: time.Now(),
			EndTime:   time.Now().Add(duration),
			Votes:     make(map[int]int),
			Voters:    make(map[string]struct{}),
		}

		// Сохраняем в хранилище
		app.pollStorage.Lock()
		app.pollStorage.Polls[pollID] = newPoll
		app.pollStorage.Unlock()

		// Формируем сообщение с опросом
		numberedOptions := make([]string, len(options))
		for i, option := range options {
			numberedOptions[i] = fmt.Sprintf("%d. %s", i+1, option)
		}

		// Формируем сообщение с результатами
		var results []string
		for i, option := range newPoll.Options {
			results = append(results, fmt.Sprintf("%d. %s", i+1, option))
		}

		response := fmt.Sprintf("**Опрос #%s**\n**%s**\n%s\n_Для голосования: /vote %s [номер]_",
			pollID,
			question,
			strings.Join(results, "\n"),
			pollID)

		sendResponse(app, post.ChannelId, response, post)
	} else if voteRegex := regexp.MustCompile(`^/vote\b`); voteRegex.MatchString(post.Message) {
		handleVoteCommand(app, post)
	} else if resultRegex := regexp.MustCompile(`^/result\b`); resultRegex.MatchString(post.Message) {
		handleResultCommand(app, post)
	} else if closeRegex := regexp.MustCompile(`^/close\b`); closeRegex.MatchString(post.Message) {
		handleCloseCommand(app, post)
	} else if deleteRegex := regexp.MustCompile(`^/delete\b`); deleteRegex.MatchString(post.Message) {
		handleDeleteCommand(app, post)
	}
}

// 2. Функция обработки удаления
func handleDeleteCommand(app *application, post *model.Post) {
	args := strings.Fields(post.Message)
	if len(args) != 2 {
		sendResponse(app, post.ChannelId, "Используйте: /delete [ID_опроса]", post)
		return
	}

	pollID := args[1]

	app.pollStorage.Lock()
	defer app.pollStorage.Unlock()

	poll, exists := app.pollStorage.Polls[pollID]
	if !exists {
		sendResponse(app, post.ChannelId, "Опрос не найден", post)
		return
	}

	// Проверяем права
	if post.UserId != poll.CreatorID {
		sendResponse(app, post.ChannelId, "Только создатель может удалить опрос", post)
		return
	}

	// Удаляем опрос
	delete(app.pollStorage.Polls, pollID)
	sendResponse(app, post.ChannelId,
		fmt.Sprintf("Опрос #%s успешно удален", pollID), post)
}

// 2. Функция обработки закрытия
func handleCloseCommand(app *application, post *model.Post) {
	args := strings.Fields(post.Message)
	if len(args) != 2 {
		sendResponse(app, post.ChannelId, "Используйте: /close [ID_опроса]", post)
		return
	}

	pollID := args[1]

	app.pollStorage.Lock()
	defer app.pollStorage.Unlock()

	poll, exists := app.pollStorage.Polls[pollID]
	if !exists {
		sendResponse(app, post.ChannelId, "Опрос не найден", post)
		return
	}

	// Проверяем права
	if post.UserId != poll.CreatorID {
		sendResponse(app, post.ChannelId, "Только создатель может закрыть опрос", post)
		return
	}

	// Закрываем досрочно
	if time.Now().Before(poll.EndTime) {
		poll.EndTime = time.Now()
		app.pollStorage.Polls[pollID] = poll
		sendResponse(app, post.ChannelId,
			fmt.Sprintf("Опрос #%s закрыт досрочно", pollID), post)
	} else {
		sendResponse(app, post.ChannelId,
			"Опрос уже завершен", post)
	}
}

func handleResultCommand(app *application, post *model.Post) {
	args := strings.Fields(post.Message)
	if len(args) != 2 {
		sendResponse(app, post.ChannelId, "Используйте: /result [ID_опроса]", post)
		return
	}

	pollID := args[1]

	app.pollStorage.RLock()
	poll, exists := app.pollStorage.Polls[pollID]
	app.pollStorage.RUnlock()

	status := "активно"
	if time.Now().After(poll.EndTime) {
		status = "завершено"
	}

	if !exists {
		sendResponse(app, post.ChannelId, "Опрос не найден", post)
		return
	}

	// Формируем результаты
	var results []string
	totalVotes := 0
	for i, option := range poll.Options {
		votes := poll.Votes[i]
		totalVotes += votes
		results = append(results, fmt.Sprintf("%d. %s - %d голосов", i+1, option, votes))
	}

	response := fmt.Sprintf("**Результаты опроса #%s** (%s)\n%s\n\nВсего голосов: %d",
		pollID,
		status,
		strings.Join(results, "\n"),
		totalVotes)

	if !poll.EndTime.IsZero() {
		response += fmt.Sprintf("\nВремя окончания: %s",
			poll.EndTime.Format("2006-01-02 15:04"))
	}

	sendResponse(app, post.ChannelId, response, post)
}

// 3. Функция обработки голосования
func handleVoteCommand(app *application, post *model.Post) {
	args := strings.Fields(post.Message)
	if len(args) != 3 {
		sendResponse(app, post.ChannelId, "Используйте: /vote [ID_опроса] [номер_варианта]", post)
		return
	}

	pollID := args[1]
	choiceStr := args[2]

	// Преобразуем номер варианта в число
	choice, err := strconv.Atoi(choiceStr)
	if err != nil || choice < 1 {
		sendResponse(app, post.ChannelId, "Некорректный номер варианта", post)
		return
	}

	app.pollStorage.RLock()
	poll, exists := app.pollStorage.Polls[pollID]
	app.pollStorage.RUnlock()

	if !exists {
		sendResponse(app, post.ChannelId, "Опрос не найден", post)
		return
	}

	// Проверяем существование варианта
	if choice > len(poll.Options) {
		sendResponse(app, post.ChannelId,
			fmt.Sprintf("Вариант %d не существует", choice), post)
		return
	}

	// Проверяем время
	if time.Now().After(poll.EndTime) {
		sendResponse(app, post.ChannelId,
			"Голосование закрыто. Время истекло.", post)
		return
	}

	// Проверяем, не голосовал ли уже пользователь
	app.pollStorage.Lock()
	defer app.pollStorage.Unlock()

	if _, voted := poll.Voters[post.UserId]; voted {
		sendResponse(app, post.ChannelId, "Вы уже голосовали в этом опросе", post)
		return
	}

	// Обновляем голоса
	poll.Votes[choice-1]++ // Варианты нумеруются с 1
	poll.Voters[post.UserId] = struct{}{}

	// Сохраняем обновленный опрос
	app.pollStorage.Polls[pollID] = poll

	sendResponse(app, post.ChannelId, "Ваш голос учтен!", post)
}

func splitArgs(input string) ([]string, error) {
	var args []string
	current := ""
	inQuotes := false
	input = strings.TrimSpace(input)

	for i, r := range input {
		switch {
		case r == '"':
			// Добавляем кавычку в результат
			current += string(r)
			if i > 0 && input[i-1] == '\\' {
				// Экранированная кавычка внутри аргумента
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
		return nil, fmt.Errorf("unclosed quotes")
	}

	return args, nil
}

// Функция помощи
func sendHelp(app *application, channelId string, originalPost *model.Post) {
	helpText := `Некорректный формат команды. Пример использования:
/poll 2h "Лучший язык?" "Go" "Python"
`
	sendResponse(app, channelId, helpText, originalPost)
}

//// 4. Обновляем сообщение помощи
//func sendHelp(app *application, channelId string, originalPost *model.Post) {
//	helpText := `Доступные команды:
///poll "Вопрос" "Вариант 1" "Вариант 2"... - Создать опрос
///vote [ID_опроса] [номер] - Проголосовать
///result [ID_опроса] - Показать результаты`
//
//	sendResponse(app, channelId, helpText, originalPost)
//}

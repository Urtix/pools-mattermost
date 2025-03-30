package main

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/mattermost/mattermost-server/v6/model"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func handlePost(app *Application, post *model.Post) {
	app.logger.Debug().Str("message", post.Message).Msg("")
	app.logger.Debug().Interface("post", post).Msg("")

	if pollRegex := regexp.MustCompile(`(?m)^/poll\b`); pollRegex.MatchString(post.Message) {
		handlePollCommand(app, post)
	} else if voteRegex := regexp.MustCompile(`^/vote\b`); voteRegex.MatchString(post.Message) {
		handleVoteCommand(app, post)
	} else if resultRegex := regexp.MustCompile(`^/result\b`); resultRegex.MatchString(post.Message) {
		handleResultCommand(app, post)
	} else if closeRegex := regexp.MustCompile(`^/close\b`); closeRegex.MatchString(post.Message) {
		handleCloseCommand(app, post)
	} else if deleteRegex := regexp.MustCompile(`^/delete\b`); deleteRegex.MatchString(post.Message) {
		handleDeleteCommand(app, post)
	} else if helpRegex := regexp.MustCompile(`^/phelp\b`); helpRegex.MatchString(post.Message) {
		handleHelpCommand(app, post)
	}
}

// Создание голосования
func handlePollCommand(app *Application, post *model.Post) {
	args, err := splitArgs(post.Message)
	if err != nil {
		sendResponse(app, post.ChannelId, "Ошибка: Незакрытые кавычки", post)
		return
	}

	duration, err := time.ParseDuration(args[1])
	if err != nil {
		sendResponse(app, post.ChannelId,
			"Некорректный формат времени. Пример: 30m, 2h, 24h", post)
		return
	}

	// Проверяем наличие кавычек для каждого аргумента после времени
	for _, arg := range args[2:] {
		if !strings.HasPrefix(arg, `"`) || !strings.HasSuffix(arg, `"`) {
			fmt.Println(arg)
			sendHelp(app, post.ChannelId, post)
			return
		}
	}

	// Проверяем минимальное количество аргументов (команда + вопрос + время + минимум 2 варианта)
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

	response := fmt.Sprintf("**Опрос #%s**\n**%s**\n%s\n_Для голосования: /vote %s [номер]_",
		pollID,
		question,
		strings.Join(numberedOptions, "\n"),
		pollID)

	sendResponse(app, post.ChannelId, response, post)
}

// Удаление голосования
func handleDeleteCommand(app *Application, post *model.Post) {
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

// Преждевременная остановка голосования
func handleCloseCommand(app *Application, post *model.Post) {
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

// Получение результатов голосования
func handleResultCommand(app *Application, post *model.Post) {
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

// Отправка голоса
func handleVoteCommand(app *Application, post *model.Post) {
	args := strings.Fields(post.Message)
	if len(args) != 3 {
		sendResponse(app, post.ChannelId, "Используйте: /vote [ID_опроса] [номер_варианта]", post)
		return
	}

	pollID := args[1]
	choiceStr := args[2]

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
			"Голосование завершено", post)
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

func handleHelpCommand(app *Application, post *model.Post) {
	helpText := `Доступные команды:

/poll [время] "Вопрос" "Вариант 1" "Вариант 2" ... - Создать опрос
Пример: /poll 2h "Лучший язык?" "Go" "Python"

/vote [ID_опроса] [номер] - Проголосовать за вариант
Пример: /vote abc123 1

/result [ID_опроса] - Показать текущие результаты
Пример: /result abc123

/close [ID_опроса] - Закрыть опрос досрочно (только автор)
/delete [ID_опроса] - Удалить опрос (только автор)
/phelp - Показать эту справку`

	sendResponse(app, post.ChannelId, helpText, post)
}

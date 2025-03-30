package main

import (
	"fmt"
	_ "github.com/joho/godotenv/autoload"
	"github.com/mattermost/mattermost-server/v6/model"
	"github.com/rs/zerolog"
	"os"
	"time"
)

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

	app.mattermostClient = model.NewAPIv4Client(app.config.mattermostServer.String())

	app.mattermostClient.SetToken(app.config.mattermostToken)

	if user, resp, err := app.mattermostClient.GetUser("me", ""); err != nil {
		app.logger.Fatal().Err(err).Msg("Could not log in")
	} else {
		app.logger.Debug().Interface("user", user).Interface("resp", resp).Msg("")
		app.logger.Info().Msg("Logged in to mattermost")
		app.mattermostUser = user
	}

	if team, resp, err := app.mattermostClient.GetTeamByName(app.config.mattermostTeamName, ""); err != nil {
		app.logger.Fatal().Err(err).Msg("Could not find team. Is this bot a member ?")
	} else {
		app.logger.Debug().Interface("team", team).Interface("resp", resp).Msg("")
		app.mattermostTeam = team
	}

	listenToEvents(app)
}

package main

import (
	"github.com/mattermost/mattermost-server/v6/model"
	"github.com/rs/zerolog"
)

type Application struct {
	config                    config
	logger                    zerolog.Logger
	mattermostClient          *model.Client4
	mattermostWebSocketClient *model.WebSocketClient
	mattermostUser            *model.User
	mattermostTeam            *model.Team
	pollStorage               *PollStorage
}

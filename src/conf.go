package main

import (
	"net/url"
	"os"
)

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

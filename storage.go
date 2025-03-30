package main

import (
	"sync"
	"time"
)

type Poll struct {
	ID        string
	Question  string
	Options   []string
	Votes     map[int]int
	Voters    map[string]struct{}
	CreatorID string
	ChannelID string
	CreatedAt time.Time
	EndTime   time.Time
}

type PollStorage struct {
	sync.RWMutex
	Polls map[string]Poll
}

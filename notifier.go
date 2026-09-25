package main

import "log"

type LogNotifier struct{}

func NewLogNotifier() *LogNotifier { return &LogNotifier{} }

func (LogNotifier) Send(n journeyNotification) error {
	log.Printf("NOTIFICATION journey=%s event=%s leg=%d title=%s body=%s",
		n.JourneyID, n.EventType, n.LegSequence, n.Title, n.Body)
	return nil
}

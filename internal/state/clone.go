package state

func cloneFileData(src fileData) fileData {
	dst := fileData{
		Version: src.Version,
		Auth:    src.Auth,
	}
	if src.Receipts != nil {
		dst.Receipts = make(map[string]ReceiptRecord, len(src.Receipts))
		for key, record := range src.Receipts {
			dst.Receipts[key] = record
		}
	}
	if src.NotificationOutbox != nil {
		dst.NotificationOutbox = make(map[string]NotificationEvent, len(src.NotificationOutbox))
		for key, event := range src.NotificationOutbox {
			dst.NotificationOutbox[key] = cloneNotificationEvent(event)
		}
	}
	return dst
}

func cloneNotificationEvent(event NotificationEvent) NotificationEvent {
	cloned := event
	if event.LastAttemptAt != nil {
		lastAttemptAt := *event.LastAttemptAt
		cloned.LastAttemptAt = &lastAttemptAt
	}
	if event.DeliveredAt != nil {
		deliveredAt := *event.DeliveredAt
		cloned.DeliveredAt = &deliveredAt
	}
	return cloned
}

func normalizeFileData(data *fileData) bool {
	changed := false
	if data.Receipts == nil {
		data.Receipts = make(map[string]ReceiptRecord)
		changed = true
	}
	if data.NotificationOutbox == nil {
		data.NotificationOutbox = make(map[string]NotificationEvent)
		changed = true
	}
	return changed
}

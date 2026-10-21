package pebblestore

func sessionIDs(sessions []SessionSnapshot) []string {
	ids := make([]string, 0, len(sessions))
	for _, session := range sessions {
		ids = append(ids, session.ID)
	}
	return ids
}

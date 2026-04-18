package ui

func (p *ChatPage) SetSwarmNotificationCount(count int) {
	if p == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	p.swarmNotificationCount = count
}

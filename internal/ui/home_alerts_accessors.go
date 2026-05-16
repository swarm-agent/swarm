package ui

func (p *HomePage) AlertsModalVisible() bool {
	return p != nil && p.alertsModal.Visible
}

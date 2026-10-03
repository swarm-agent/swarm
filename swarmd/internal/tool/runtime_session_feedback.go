package tool

import "fmt"

// sessionMessageTrigger fails closed on ambiguous or malformed execution intent.
func sessionMessageTrigger(args map[string]any) (bool, error) {
	trigger := true
	seen := false
	for _, key := range []string{"trigger_run", "trigger"} {
		value, exists := args[key]
		if !exists {
			continue
		}
		v, ok := value.(bool)
		if !ok {
			return false, fmt.Errorf("%s must be boolean; rejected without mutation", key)
		}
		if seen && trigger != v {
			return false, fmt.Errorf("trigger and trigger_run conflict; rejected without mutation")
		}
		trigger, seen = v, true
	}
	return trigger, nil
}

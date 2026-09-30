package config

import "os"

// Environment snapshots process inputs once at the command boundary.
func Environment() map[string]string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		for i := 0; i < len(entry); i++ {
			if entry[i] == '=' {
				values[entry[:i]] = entry[i+1:]
				break
			}
		}
	}
	return values
}

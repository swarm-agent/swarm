//go:build !unix

package appstorage

func OwnedByAnother(string) bool { return false }

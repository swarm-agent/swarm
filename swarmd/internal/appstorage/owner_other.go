//go:build !unix

package appstorage

func ownedByAnother(string) bool { return false }

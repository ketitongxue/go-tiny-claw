// Package promptinfo contains prompt contracts, independent of tool schemas and implementations.
package promptinfo

type Tool struct {
	Name       string
	Summary    string
	Guidelines []string
}

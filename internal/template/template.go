// Package template renders constrained destination name templates.
package template

// Context contains values available to destination name templates.
type Context struct {
	MountAccessor   string
	Mount           string
	Path            string
	Version         int
	DestinationType string
	DestinationName string
}

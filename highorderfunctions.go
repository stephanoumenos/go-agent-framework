// ./highorderfunctions.go
package heart

import (
	"errors"
	"fmt"
)

/*
NOTE: Map, ForEach, Filter are placeholders and likely BROKEN with the eager execution model
and removal of ResolverContext. They need significant redesign to work correctly,
potentially involving dynamic node creation or different execution strategies.
Marking them as such.
*/

// Map - BROKEN: Needs redesign for eager execution and context/path handling.
func Map[SIn ~[]In, In any, SOut ~[]Out, Out any](s Output[SIn], fun func(In) Output[Out]) Output[SOut] {
	fmt.Println("WARNING: heart.Map is currently broken and needs redesign.")
	// Placeholder: returns an error immediately
	return IntoError[SOut](errors.New("heart.Map is not implemented for eager execution"))
}

// ForEach - BROKEN: Needs redesign for eager execution and context/path handling.
func ForEach[SIn ~[]In, In any](s Output[SIn], fun func(In) Output[struct{}]) Output[struct{}] {
	fmt.Println("WARNING: heart.ForEach is currently broken and needs redesign.")
	// Placeholder: returns an error immediately
	return IntoError[struct{}](errors.New("heart.ForEach is not implemented for eager execution"))
}

// Filter - BROKEN: Needs redesign for eager execution and context/path handling.
func Filter[SIn ~[]In, In any](s Output[SIn], fun func(In) Output[bool]) Output[SIn] {
	fmt.Println("WARNING: heart.Filter is currently broken and needs redesign.")
	// Placeholder: returns an error immediately
	return IntoError[SIn](errors.New("heart.Filter is not implemented for eager execution"))
}

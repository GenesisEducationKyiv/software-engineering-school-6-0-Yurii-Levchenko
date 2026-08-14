// Package architecture holds no runtime code — only architecture dependency
// tests (arch_test.go). They encode the layering rules from
// system-design/architecture.md as executable assertions over the package
// import graph, so a violation (e.g. the domain importing the transport layer,
// or the monolith importing net/smtp) fails CI instead of silently rotting.
package architecture

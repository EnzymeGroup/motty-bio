// Package buildinfo names the program variant selected at build time.
package buildinfo

// ProductName is the user-visible name of this build.
const ProductName = productName

// DataName separates development data from the installed application's data.
const DataName = dataName

// Development reports whether this binary was built with the dev tag.
const Development = development

// DockBadge is the label the build puts in the red badge macOS draws at the
// corner of the application's Dock icon. A development build marks the running
// copy this way instead of renaming the product inside its own interface; an
// empty label means no badge.
const DockBadge = dockBadge

// VariantSuffix marks this local derivative build based on the upstream version
// (e.g. 0.4.24 -> 0.4.24-bio.1) without colliding with the next upstream patch.
const VariantSuffix = "-bio.3"

// DisplayVersion returns base with VariantSuffix appended.
func DisplayVersion(base string) string {
	return base + VariantSuffix
}

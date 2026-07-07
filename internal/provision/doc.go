// Package provision ports the original bash provisioner's orchestration into
// Go: it
// renders the Lima base overlay, builds the phased Ansible extra-vars, locates
// the playbook, and drives the base-build -> clone -> finalize -> bounce
// sequence through a lima.Client while streaming output.
package provision

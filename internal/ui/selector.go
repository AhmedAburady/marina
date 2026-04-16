package ui

import (
	"charm.land/huh/v2"
)

// SelectHost shows an interactive host picker.
// Returns the selected host name, or an empty string if "All hosts" was chosen.
func SelectHost(hostNames []string) (string, error) {
	options := make([]huh.Option[string], 0, len(hostNames)+1)
	options = append(options, huh.NewOption("All hosts", ""))
	for _, name := range hostNames {
		options = append(options, huh.NewOption(name, name))
	}

	var selected string
	sel := huh.NewSelect[string]().
		Title("Select a host").
		Options(options...).
		Value(&selected)

	err := huh.NewForm(huh.NewGroup(sel)).Run()
	if err != nil {
		return "", err
	}
	return selected, nil
}

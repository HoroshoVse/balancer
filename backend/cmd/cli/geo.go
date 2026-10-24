package main

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/balancer/backend/internal/geo"
)

// dataDir is where the server keeps its files (the volume mapped in docker-compose).
func dataDir() string {
	if d := os.Getenv("DATA_DIR"); d != "" {
		return d
	}
	return "data"
}

func printGeoUsage() {
	fmt.Println("Usage: balancer-cli geo <action> [args...]")
	fmt.Println("\nCountry database (official delegation files of the five regional registries):")
	fmt.Println("  geo status                               Show what is downloaded and how old it is")
	fmt.Println("  geo update                               Download or refresh the database now")
	fmt.Println("  geo lookup <ip>                          Show the country of an address")
	fmt.Println("  geo export <RU,BY,...> [format] [name]   Print the subnets of countries for a firewall.")
	fmt.Println("                                           format: plain (default), ipset, nft; name: set name (default geo)")
	os.Exit(1)
}

// runGeo handles `balancer-cli geo ...`; it needs only the data directory, not the database.
func runGeo(action string, args []string) {
	m := geo.NewManager(filepath.Join(dataDir(), "geo"), geo.DefaultSources, func(level, msg string) {
		fmt.Fprintln(os.Stderr, msg)
	})
	if err := m.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "Reading the saved database: %v\n", err)
		os.Exit(1)
	}

	switch action {
	case "status":
		printGeoStatus(m.Status())

	case "update":
		fmt.Fprintln(os.Stderr, "Downloading from the registries; the first time this takes several minutes (they serve slowly)...")
		err := m.Update(context.Background())
		printGeoStatus(m.Status())
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nNot everything could be updated: %v\n", err)
			os.Exit(1)
		}

	case "lookup":
		if len(args) < 1 {
			printGeoUsage()
		}
		a, err := netip.ParseAddr(args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "%q is not an IP address\n", args[0])
			os.Exit(1)
		}
		cc, loaded := m.Country(a)
		switch {
		case !loaded:
			fmt.Fprintln(os.Stderr, "The database is not complete: run `geo update`.")
			os.Exit(1)
		case cc == "":
			fmt.Printf("%s: no registry lists this address\n", a)
		default:
			fmt.Printf("%s: %s\n", a, cc)
		}

	case "export":
		if len(args) < 1 {
			printGeoUsage()
		}
		if !m.Status().Loaded {
			fmt.Fprintln(os.Stderr, "The database is not complete: run `geo update` first.")
			os.Exit(1)
		}
		format, name := geo.FormatPlain, "geo"
		if len(args) > 1 {
			format = args[1]
		}
		if len(args) > 2 {
			name = args[2]
		}
		var codes []string
		for _, c := range strings.FieldsFunc(args[0], func(r rune) bool { return r == ',' || r == ' ' }) {
			codes = append(codes, strings.ToUpper(c))
		}
		if err := m.Database().WriteExport(os.Stdout, codes, format, name); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	default:
		fmt.Printf("Unknown action for geo: %s\n", action)
		printGeoUsage()
	}
}

func printGeoStatus(st geo.Status) {
	for _, s := range st.Sources {
		state := "no data"
		if s.Present {
			state = fmt.Sprintf("%d ranges, registry date %s", s.Ranges, s.DataDate)
		}
		line := fmt.Sprintf("  %-8s %s", s.Name, state)
		if s.Error != "" {
			line += "  [" + s.Error + "]"
		}
		fmt.Println(line)
	}
	if st.Loaded {
		fmt.Printf("Complete: %d countries, %d ranges, confirmed current %s\n", st.Countries, st.Ranges, st.UpdatedAt.Local().Format("2006-01-02 15:04"))
	} else {
		fmt.Println("Incomplete: country rules stay off until every registry is present.")
	}
}

package catalog

const curatedSourceID = "curated-groups"

func curated(id, designation, name string, aliases []string, typ string, ra, dec, major, minor, pa float64) Object {
	return Object{ID: id, Designation: designation, Name: name, Aliases: aliases, Type: typ, RA: ra, Dec: dec,
		MajorArcmin: major, MinorArcmin: minor, PA: pa, Source: curatedSourceID}
}

func curatedOverlay() Overlay {
	return Overlay{
		Source: Source{
			ID: curatedSourceID, Name: "astro-stacker curated galaxy groups, clusters and asterisms",
			Citation: "Centres, extents and position angles measured from the member objects' OpenNGC positions and the bright stars' Hipparcos positions",
			URL:      "https://github.com/USA-RedDragon/astro-stacker", Licence: "MIT",
		},
		Objects: []Object{
			curated("MARKARIANSCHAIN", "Markarian's Chain", "Markarian's Chain", []string{"Markarian Chain"}, TypeGalaxyGroup, 186.89, 13.26, 90, 30, 58),
			curated("VIRGOCLUSTER", "Virgo Cluster", "Virgo Cluster", []string{"Virgo Cluster of Galaxies"}, TypeGalaxyGroup, 187.71, 12.39, 480, 480, 0),
			curated("FORNAXCLUSTER", "Fornax Cluster", "Fornax Cluster", []string{"Abell S373", "Fornax Cluster of Galaxies"}, TypeGalaxyGroup, 54.62, -35.45, 240, 240, 0),
			curated("DRACOTRIPLET", "Draco Triplet", "Draco Triplet", []string{"NGC 5985 Group"}, TypeGalaxyGroup, 234.69, 59.36, 18, 8, 97),
			curated("DEERLICKGROUP", "Deer Lick Group", "Deer Lick Group", []string{"NGC 7331 Group"}, TypeGalaxyGroup, 339.32, 34.41, 15, 10, 90),
			curated("M81GROUP", "M81 Group", "M81 Group", []string{"M81 and M82", "Bode's Galaxy and Cigar Galaxy"}, TypeGalaxyGroup, 149.5, 69.16, 75, 50, 125),
			curated("M96GROUP", "M96 Group", "M96 Group", []string{"Leo I Group", "M95, M96 and M105"}, TypeGalaxyGroup, 161.55, 12.03, 90, 40, 47),
			curated("WHALEHOCKEYSTICK", "Whale and Hockey Stick", "Whale and Hockey Stick", []string{"NGC 4631 and NGC 4656"}, TypeGalaxyGroup, 190.76, 32.36, 45, 20, 133),
			curated("KEMBLESCASCADE", "Kemble's Cascade", "Kemble's Cascade", nil, TypeOther, 59.5, 63.2, 170, 20, 127),
		},
		Patches: []Patch{
			{ID: "HCG57", Name: "Copeland's Septet"},
			{ID: "STOCK2", Name: "Muscleman Cluster"},
			{ID: "ACO1656", Aliases: []string{"Abell 1656", "Coma Cluster of Galaxies"}},
			{ID: "ACO426", Aliases: []string{"Abell 426", "Perseus Cluster of Galaxies"}},
			{ID: "ACO2151", Aliases: []string{"Abell 2151", "Hercules Cluster"}},
			{ID: "ACO1367", Aliases: []string{"Abell 1367"}},
			{ID: "CR70", Name: "Orion's Belt", Aliases: []string{"Collinder 70", "Orion's Belt Cluster"}},
		},
	}
}

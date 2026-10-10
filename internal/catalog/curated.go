package catalog

const curatedSourceID = "curated-groups"

func curatedOverlay() Overlay {
	return Overlay{
		Source: Source{
			ID: curatedSourceID, Name: "Kemble's Cascade from Wikipedia, and astro-stacker curated cluster nicknames",
			Citation: "Kemble's Cascade centre (RA 04h00m, Dec +63°00′) and length (~3°) from the Wikipedia infobox, revision 1373378943; its width and position angle are not catalogued. Cluster nicknames compiled for astro-stacker",
			URL:      "https://en.wikipedia.org/w/index.php?title=Kemble%27s_Cascade&oldid=1373378943", Licence: "CC-BY-SA-4.0",
		},
		Objects: []Object{
			{ID: "KEMBLESCASCADE", Designation: "Kemble's Cascade", Name: "Kemble's Cascade", Aliases: []string{"Kemble 1"}, Type: TypeOther, RA: 60, Dec: 63, MajorArcmin: 180, Source: curatedSourceID},
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

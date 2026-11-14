#!/bin/sh
# Regenerates internal/places from GeoNames (stratus-app#215): every place
# with more than 5,000 people that is a town rather than part of one, as name, latitude, longitude and country code,
# and the names of the countries. CC BY 4.0, credited in places/NOTICE.
#
# Deterministic, so a rerun with nothing changed upstream changes no byte:
# sorted, rounded to three places (about a hundred metres), and gzip -n.
set -eu

out=internal/places
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

curl -sSfL -o "$work/cities.zip" https://download.geonames.org/export/dump/cities5000.zip
curl -sSfL -o "$work/countryInfo.txt" https://download.geonames.org/export/dump/countryInfo.txt
unzip -q -d "$work" "$work/cities.zip" cities5000.txt

# Towns and not parts of one: PPLX is a section of a city -- the nearest
# point in Barcelona is a neighbourhood -- and the historical, abandoned and
# destroyed places are not where anybody is now.
awk -F'\t' '$8 !~ /^PPL[XHQW]$/ { printf "%s\t%.3f\t%.3f\t%s\n", $2, $5, $6, $9 }' "$work/cities5000.txt" |
	LC_ALL=C sort -t "$(printf '\t')" -k4,4 -k1,1 -k2,2 |
	gzip -9 -n >"$out/cities.tsv.gz"
grep -v '^#' "$work/countryInfo.txt" | awk -F'\t' '{ print $1 "\t" $5 }' | LC_ALL=C sort >"$out/countries.tsv"

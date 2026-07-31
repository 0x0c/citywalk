.PHONY: site serve clean

# The roadmap site, generated from roadmaps/ into site/. Standard library only, so there is nothing
# to install first.
site:
	python3 scripts/build_roadmap_site.py

# Build, then serve site/ on http://127.0.0.1:8000 for a local look.
serve: site
	python3 -m http.server 8000 --directory site

clean:
	rm -rf site

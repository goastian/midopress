# Test fonts

All fonts are licensed under the SIL Open Font License 1.1 (OFL). They are used
here only as test data. Sizes are in bytes.

| File | Size | What it exercises | Source | Licence |
| --- | --- | --- | --- | --- |
| `AnonymousPro-Regular.ttf` | 158100 | hinted TrueType (`glyf`, `fpgm`, `prep`, `cvt `), embedded bitmaps (`EBDT`/`EBLC`/`EBSC`), `hdmx`, `VDMX`, `LTSH` | https://raw.githubusercontent.com/google/fonts/main/ofl/anonymouspro/AnonymousPro-Regular.ttf | OFL 1.1, Copyright (c) 2009 Mark Simonson |
| `KarlaTamilInclined-Regular.ttf` | 109700 | `kern` and `GPOS`/`GDEF`/`GSUB`, hinting, `hdmx`, `VDMX`, `LTSH` | https://raw.githubusercontent.com/google/fonts/main/ofl/karlatamilinclined/KarlaTamilInclined-Regular.ttf | OFL 1.1, Copyright (c) 2011-2012 Jonathan Pinhorn |
| `Comfortaa-VF.ttf` | 201756 | variable font (`fvar`, `gvar`, `avar`, `HVAR`, `STAT`), `DSIG`, `GPOS` | https://raw.githubusercontent.com/google/fonts/main/ofl/comfortaa/Comfortaa%5Bwght%5D.ttf | OFL 1.1, Copyright 2011 The Comfortaa Project Authors |
| `SourceSans3-Regular-Latin.otf` | 66036 | CFF-based OpenType (`CFF `, `BASE`, `GPOS`, `GSUB`, `GDEF`) | Latin subset of https://raw.githubusercontent.com/adobe-fonts/source-sans/release/OTF/SourceSans3-Regular.otf | OFL 1.1, Copyright 2010-2024 Adobe |
| `PressStart2P-Regular.woff` | 39388 | WOFF1 container (zlib tables), `DSIG` | converted to WOFF1 from https://raw.githubusercontent.com/google/fonts/main/ofl/pressstart2p/PressStart2P-Regular.ttf | OFL 1.1, Copyright 2012 The Press Start 2P Project Authors |

Derivations (fontTools 4.67.0, run in a throwaway virtualenv):

```
pyftsubset SourceSans3-Regular.otf --unicodes=U+0020-007E,U+00A0-00FF,U+2013-2014,U+2018-201D,U+20AC \
    --layout-features='*' --name-IDs='*' --notdef-outline --output-file=SourceSans3-Regular-Latin.otf
python -c "from fontTools.ttLib import TTFont; t=TTFont('PressStart2P-Regular.ttf'); t.flavor='woff'; t.save('PressStart2P-Regular.woff')"
```

The Source Sans subset is a modified version of a font with a Reserved Font
Name; it is kept here for testing only and must not be redistributed as a
font.

# Icon

An arch over a deck: one span carrying home across to wherever you are. Colours match the
homenet welcome site (night `#1B2440`, lamp `#FFB347`, deck `#EEF1FA`; lamp on light
backgrounds `#A35F00`).

| File | Use |
|---|---|
| `icon.svg` | Master, 96 px and up (README, launcher, social preview) |
| `icon-32.svg` | 24–48 px: no hangers, heavier strokes |
| `icon-16.svg` | 16 px favicon |
| `icon-light.svg` | Light tile, for dark or busy backgrounds |
| `glyph.svg` | One colour (`currentColor`), no tile: notifications, monochrome favicons, icon tiles |
| `social-preview.svg` / `.png` | GitHub social preview, 1280×640 |
| `png/` | Rendered sizes |

Re-render the PNGs after changing an SVG:

```sh
docker run --rm -v "$PWD/assets/icon":/i -w /i alpine sh -c 'apk add -q librsvg font-noto && \
  for s in 512 192 128; do rsvg-convert -w $s icon.svg -o png/icon-$s.png; done && \
  rsvg-convert -w 32 icon-32.svg -o png/icon-32.png && rsvg-convert -w 48 icon-32.svg -o png/icon-48.png && \
  rsvg-convert -w 16 icon-16.svg -o png/icon-16.png && rsvg-convert -w 512 icon-light.svg -o png/icon-light-512.png && \
  sed s/currentColor/#ffffff/g glyph.svg | rsvg-convert -w 96 -o png/glyph-white-96.png && \
  rsvg-convert social-preview.svg -o social-preview.png'
```

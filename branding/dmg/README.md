# DMG background

`background.png` — Claude Monet, *Regatta at Argenteuil* (c. 1872), Musée
d'Orsay. Public domain (artist died 1926); scan via the Google Art Project on
[Wikimedia Commons](https://commons.wikimedia.org/wiki/File:Claude_Monet_-_Regattas_at_Argenteuil_-_Google_Art_Project.jpg).
Cropped to a 720x430pt window (2x, 1440x860px). The icon layout in
`scripts/package/macos.sh` places both icons at the max 128pt size in the
open sky above the sails/treeline — left icon centered at (165,85)pt, right
at (520,85)pt. Those coordinates were chosen so the icons clear the sails
and the house roof at full size (checked empirically, not just eyeballed);
moving them lower or further apart will start clipping the painting.
Regenerate by re-cropping the source scan if the window size changes.

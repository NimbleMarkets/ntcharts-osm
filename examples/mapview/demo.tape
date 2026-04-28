# NOTE: hand-make this GIF — Kitty Graphics do not render in VHS,
# and the half-block render path is what you can capture cleanly here.

Output demo.gif

Require echo

Set Shell "bash"
Set Padding 0
Set Framerate 10
Set PlaybackSpeed 1.0
Set FontSize 8
Set Width 800
Set Height 480

Hide
Type "./bin/ntcharts-osm-mapview"
Enter
Show

Sleep 1.5s
Type "+"
Sleep 800ms
Type "+"
Sleep 800ms
Type "l"
Sleep 600ms
Type "l"
Sleep 600ms
Type "j"
Sleep 600ms
Type "s"
Sleep 1s
Type "s"
Sleep 1s
Type "-"
Sleep 800ms
Type "-"

Hide
Type "q"

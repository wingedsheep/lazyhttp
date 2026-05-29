# VHS tape to capture the README screenshot.
#   vhs .github/assets/screenshot.tape
# Produces .github/assets/screenshot.png

Output .github/assets/screenshot.gif

Set Shell "bash"
Set FontSize 15
Set Width 1500
Set Height 880
Set Padding 24
Set Margin 0

Hide
Type "bin/lazy-http --env dev example.http"
Enter
Sleep 2s
Show

# Run the @reset anchor on its own (it clears state and stops the chain),
Type "a"
Sleep 2s
# then drop to step 2 and run the rest of the plan; captures flow forward.
Type "j"
Sleep 500ms
Type "a"
Sleep 28s

# Land on "Create product": a compact 201 body with assertions + captured vars.
Type "jjjj"
Sleep 2s

Screenshot .github/assets/screenshot.png
Sleep 1s

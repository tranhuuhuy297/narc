# Renders docs/demo.gif with https://github.com/charmbracelet/vhs
#   go build -o narc ./cmd/narc && PATH=$PWD:$PATH vhs docs/demo.tape
Output docs/demo.gif

Set Shell "bash"
Set FontSize 16
Set Width 1180
Set Height 920
Set Padding 24
Set Theme "Catppuccin Mocha"
Set TypingSpeed 45ms
Set CursorBlink false

Hide
Type "export PS1='$ ' COLUMNS=104 && cd $(./examples/demo-setup.sh) && clear"
Enter
Sleep 1s
Show

Type "# agent: Fixed it. All tests pass and CI is green."
Sleep 900ms
Enter
Sleep 400ms
Type "narc"
Sleep 400ms
Enter
Sleep 7s

# screenshot-cam

## subprocess control

This module needs to interact with the user desktop (i.e. take a screenshot) from a service context. In windows this is tricky because services are in session 0 and the user is in session 1.

We get around this by creating a subprocess

To test subprocess control in a command prompt, I think you need psexec from the pstools suite; I've been testing by:
1. start -> cmd -> right click -> run as administrator
1. in that administrator shell, start a new LocalSystem shell with `..\Downloads\PSTools\PsExec.exe -i -s cmd`
1. in that system shell, my stuff works. without it, one of `WTSQueryUserToken` or `WTSGetActiveConsoleSessionId` fails with 'required privilege is not held by the client' or something

# Security policy

narc runs locally, makes no network calls and never uploads code. Agent hooks
read the hook payload on stdin and the files of the current repository.

If you find a security problem — for example a way to make narc execute code,
write outside the repository, or let an agent bypass a hook silently — please
report it privately through GitHub's "Report a vulnerability" button on the
Security tab rather than in a public issue. You will get an answer within a
few days.

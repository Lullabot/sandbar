# Changelog

## [0.4.1](https://github.com/Lullabot/sandbar/compare/v0.4.0...v0.4.1) (2026-07-15)


### Bug Fixes

* **tmux:** raise guest escape-time to 10ms for WAN transports ([70952fc](https://github.com/Lullabot/sandbar/commit/70952fc8650855f1128a25f28b1d367702debcbc))
* **tui:** lift the dimmest greys to a readable contrast floor ([7d1c787](https://github.com/Lullabot/sandbar/commit/7d1c787eded45bf15c4904ea0c1001bbe54db99b))

## [0.4.0](https://github.com/Lullabot/sandbar/compare/v0.3.0...v0.4.0) (2026-07-14)


### ⚠ BREAKING CHANGES

* **shell:** S now lands you in tmux (prefix C-a), not a bare shell. Closing the terminal detaches instead of ending the session. There is no --no-tmux escape hatch, by decision.

### Features

* **lima:** add the guest tmux attach-command builder ([78a8eef](https://github.com/Lullabot/sandbar/commit/78a8eefb1c2658ce3c6efece7c8c196d613d3645))
* **provision:** add per-phase timings and enable ansible task profiling ([6bd73dd](https://github.com/Lullabot/sandbar/commit/6bd73dd0a949c57cc34c53342bdf92347876c757))
* **provision:** base tool-set, 30-day self-refresh, and a host apt cache ([b447afa](https://github.com/Lullabot/sandbar/commit/b447afa4e37cd5a5b270076ff1b27a32798bfe8f))
* **provision:** bootstrap ansible-core and stamp the base by playbook content ([9e86b4f](https://github.com/Lullabot/sandbar/commit/9e86b4f196385eb974b022c76c634b530c3f873e))
* **provision:** one apt transaction, and in-place base re-apply under the lock ([0ad221f](https://github.com/Lullabot/sandbar/commit/0ad221fd295cb25fa3d0546ce6da468e3ecba9ac))
* **shell:** attach both entrypoints to the guest's persistent tmux session ([e09c365](https://github.com/Lullabot/sandbar/commit/e09c365e93c2e49693bb2bdd0122bb75ad520ebd))
* **toolset:** default the tool-set to what the base was actually built with ([36fda44](https://github.com/Lullabot/sandbar/commit/36fda44b74ef53a7e90aab3f6cf1ccf252ce080b))
* **toolset:** make Claude Code optional, like the other dev tooling ([67c7baf](https://github.com/Lullabot/sandbar/commit/67c7baf59e0635d71a41eab0a5acdf5d2d43bf99))
* **ui,provision,ci:** tool-set toggles, conditional bounce, and a CI reuse path ([79998f5](https://github.com/Lullabot/sandbar/commit/79998f580ec7ad76c10b14c472d1e6745ca4ef53))
* **ui:** enter does the obvious thing for the tile it is on ([c6370a8](https://github.com/Lullabot/sandbar/commit/c6370a872b176886eefc3c4a138f8fe761632375))
* **ui:** frame the messages strip in a titled "Messages" box ([e2a5d84](https://github.com/Lullabot/sandbar/commit/e2a5d84617ff74155159a1dc3ca3180104162857))
* **ui:** move the Messages box below the tiles ([8cb6f2c](https://github.com/Lullabot/sandbar/commit/8cb6f2c80ceabeec8040dfba8d136ffefb4b52e5))
* **ui:** the Messages box shows up to 10 lines ([4897cd2](https://github.com/Lullabot/sandbar/commit/4897cd2d6ff545546975597224e1a140fb43c1e7))


### Bug Fixes

* **lima:** drop ssh debug chatter from copy output ([e5edf2e](https://github.com/Lullabot/sandbar/commit/e5edf2e46008bc419ca3481b307c0004eac4543a))
* **provision,ui,ci:** nine defects from the high-effort review of this branch ([926c8d9](https://github.com/Lullabot/sandbar/commit/926c8d99767625792f185f6df9be5aa88ffc3f32))
* **provision:** a cancelled create cleans up the VM it half-created ([184e33e](https://github.com/Lullabot/sandbar/commit/184e33ea0e40340bc0da58794fef7df2edc61ebf))
* **provision:** install python3-passlib, or the user role can't hash a password ([75be96f](https://github.com/Lullabot/sandbar/commit/75be96f40fd4d3625044867c973d0b235d66806c))
* **provision:** pass --no-install-recommends, or ansible-core drags the bundle back ([00e5e52](https://github.com/Lullabot/sandbar/commit/00e5e52da475a30ed5dd1222d4353af297b1920b))
* **shell:** `sand shell` works while another VM is being created ([a45e00a](https://github.com/Lullabot/sandbar/commit/a45e00a49faa601f2d1c7031b6ed72a32b2167c1))
* **ui:** a cancelled build whose VM was cleaned up leaves no tile ([4b7483f](https://github.com/Lullabot/sandbar/commit/4b7483f2a36cad1feab1910064adc46ab97fb3b3))
* **ui:** line the Messages box up with the tiles ([9b1bf0f](https://github.com/Lullabot/sandbar/commit/9b1bf0f0b48678d5996f2820da2bc72219e629aa))
* **ui:** stop printing every message twice on the board ([e30c5b8](https://github.com/Lullabot/sandbar/commit/e30c5b87b8472a5aa2d782fbf96e297817612bb7))
* **ui:** the log box lost its right-hand border to a clip ([6efae08](https://github.com/Lullabot/sandbar/commit/6efae082fd3da4a0a3a302900e5f5897ac128cdb))


### Performance Improvements

* **base:** install with recommends off, and name the toolchain explicitly ([c44e192](https://github.com/Lullabot/sandbar/commit/c44e1920d6bea253e55e7214767bcc5879ec0eeb))
* **overlay:** switch off Lima's containerd — ~19s per boot, ~575MB per base ([70b341a](https://github.com/Lullabot/sandbar/commit/70b341a8953a71a41f1165feb79a8db996207ff9))

## [0.3.0](https://github.com/Lullabot/sandbar/compare/v0.2.0...v0.3.0) (2026-07-13)


### Features

* **tui:** board chrome — header, messages ring, state-gated footer, refresh tick ([819177d](https://github.com/Lullabot/sandbar/commit/819177d04a42ab3e1af7732b9e3294695814310f))
* **tui:** command registry, layout classifier, job registry, guest heartbeat ([7f04be5](https://github.com/Lullabot/sandbar/commit/7f04be576f6e372df59dbbab2af99e21a5568987))
* **tui:** delete the VM screen; the tile carries the core count ([6257d84](https://github.com/Lullabot/sandbar/commit/6257d84053b7c969ac6930a11511649d2df7a331))
* **tui:** live host readout, a selectable empty slot, and steady gauges ([5c90d52](https://github.com/Lullabot/sandbar/commit/5c90d524e11b390d08848f80c7fbbd6625810d13))
* **tui:** replace the table list with the tile board ([2afc1c3](https://github.com/Lullabot/sandbar/commit/2afc1c35d188e9864e9e73eca01a5816aec71263))
* **tui:** show the build, wrap the footer, and add a ? keys screen ([11343ef](https://github.com/Lullabot/sandbar/commit/11343ef2f2804231a0413c0f9b7274f061312dcf))
* **tui:** the header's cpu is a percentage of the whole host ([8bdae6e](https://github.com/Lullabot/sandbar/commit/8bdae6eca42c02b5b594dad0affe81d4753ef6ac))
* **tui:** the tile — derived status, honest gauges, exception-only fields ([6d5c289](https://github.com/Lullabot/sandbar/commit/6d5c289b031b55e126a5615f79123cf74497b4c1))


### Bug Fixes

* close the races the last review's fixes left open ([e8701de](https://github.com/Lullabot/sandbar/commit/e8701ded01819487d31c4d5a51f19848fcc22b6b))
* **lima:** pin the copy backend so files land where the user put them ([67708a5](https://github.com/Lullabot/sandbar/commit/67708a59652e6ba364ec05546fa228ebde8b6607))
* **lima:** survive limactl list failing while an instance is cloned or deleted ([19cc742](https://github.com/Lullabot/sandbar/commit/19cc742585da5d81b7c40cb4eb6c491b2f3aad0c))
* **provision:** serialize base-image preparation with a file lock ([d7507fd](https://github.com/Lullabot/sandbar/commit/d7507fdc575fa8e7fbc199a94d12dbbb0d9b92d5))
* **test:** pin the host probes for every teatest golden, not just some ([44bff14](https://github.com/Lullabot/sandbar/commit/44bff1466d6cb687bdf2824837efd4612eabd185))
* **test:** stop the unit suite writing the developer's real ~/.lima ([53fd1f4](https://github.com/Lullabot/sandbar/commit/53fd1f42c8b314eb3ae49380748e05a13b21b06f))
* **test:** wait on the screen a TUI test navigated to, not on text the renderer reused ([6cac3c0](https://github.com/Lullabot/sandbar/commit/6cac3c089730a41b6a6058dde8a2c017677ade7e))
* **tui:** a build streams onto its tile instead of seizing the terminal ([792b7a6](https://github.com/Lullabot/sandbar/commit/792b7a6e1897d22bd9780c49ddbd72ff3643300d))
* **tui:** keep the gauges live when the terminal loses focus ([1e6d524](https://github.com/Lullabot/sandbar/commit/1e6d524dda0a47dd7eafceb7fabaf66549c8d356))
* **tui:** re-gate the verbs the build freeze used to protect ([abbe0d9](https://github.com/Lullabot/sandbar/commit/abbe0d93c2a2ccefb92b9438ae913d83a2ab3621))
* **tui:** stop a file copy from flipping a failed build's tile back to green ([d1a3001](https://github.com/Lullabot/sandbar/commit/d1a3001a6993a014130041f4c3e6fa02e57621de))
* **user:** enable systemd linger so detached tmux survives logout ([a13b2b0](https://github.com/Lullabot/sandbar/commit/a13b2b0a58b8e912a47e22e7ec7e1b4e66ae8966))

## [0.2.0](https://github.com/Lullabot/sandbar/compare/v0.1.0...v0.2.0) (2026-07-12)


### Features

* **secrets:** git-credential wiring for recognized forge tokens ([ad8f47f](https://github.com/Lullabot/sandbar/commit/ad8f47f380d96c38fe03d1ac9f791493bb191949))
* **secrets:** scope-aware delivery and TUI editor ([194bf1b](https://github.com/Lullabot/sandbar/commit/194bf1bc030bc235d1cc5237062ab16abfaf1889))
* **secrets:** scope-aware store schema v2 with v1 migration ([1517ee6](https://github.com/Lullabot/sandbar/commit/1517ee68ebdca1ca7219516371867eac4dd61bbf))


### Bug Fixes

* **create:** wrap long form help and warnings to the terminal width ([02f18a8](https://github.com/Lullabot/sandbar/commit/02f18a809b94218a3cb9363f5760d4923f679a2a))
* **reset:** keep stderr out of the staged tar archive ([ccd07d6](https://github.com/Lullabot/sandbar/commit/ccd07d666c0d1da5be6f7e5f36712d2f99ffe39b))
* **reset:** signal a saved GitHub token with a masked placeholder ([765b2ab](https://github.com/Lullabot/sandbar/commit/765b2abb461f39b58a3903f968e72a45d00e591d))
* **secrets:** focus the editor on open so keystrokes register ([b10a42c](https://github.com/Lullabot/sandbar/commit/b10a42c6f334637520b8ee19aa10b3812a981ae3))
* **secrets:** reflow editor guidance so the view fits the terminal ([b53e97b](https://github.com/Lullabot/sandbar/commit/b53e97b67d188a56c6c944b6b938d81e67a27acc))
* **tui:** show a live spinner during stop-all and detail actions ([2062b77](https://github.com/Lullabot/sandbar/commit/2062b77ec1f3a92d80232750f50c1667a24439ac))

## 0.1.0 (2026-07-12)


### Features

* add direnv support and document per-directory GitHub tokens ([bf294c4](https://github.com/Lullabot/sandbar/commit/bf294c4fa382d83db11b13febfc5abfe8cca42ca))
* add new-vm.sh to spin up Lima VMs in one command ([30676d3](https://github.com/Lullabot/sandbar/commit/30676d338afffbdb0f4dc13ea66e196f0fca1eb6))
* add optional Claude Code webhook notification hooks ([#13](https://github.com/Lullabot/sandbar/issues/13)) ([728366b](https://github.com/Lullabot/sandbar/commit/728366b2841c3f7583fb71d9a33869cc331a818f))
* add project role to clone an initial repo on provision ([62c5789](https://github.com/Lullabot/sandbar/commit/62c5789e4a6d73a47fe38e424b6e3e2909fb064a))
* automatically recreate base images ([d57f72f](https://github.com/Lullabot/sandbar/commit/d57f72f52bfa6a8ec67c7af4d44852a4220cd44e))
* **base:** accept COLORTERM over SSH for truecolor in the VM ([a8f39a8](https://github.com/Lullabot/sandbar/commit/a8f39a81648c50203e033433f99835f537b9a230))
* **base:** install ncurses-term for non-xterm terminfo ([2d1bb1b](https://github.com/Lullabot/sandbar/commit/2d1bb1b29a2af54dba79d0e029c56514562501ac))
* **claude-code:** default the in-VM Claude Code theme to auto ([960b8b1](https://github.com/Lullabot/sandbar/commit/960b8b1bf1c8370bc2ea3bb1d82ce9f304b39b77))
* **claude-code:** enable remote control at startup by default ([1267958](https://github.com/Lullabot/sandbar/commit/12679586cdf8dcfdbc94dd9602be1c79b4cda378))
* configure direnv to load .env files and update docs ([d82bc02](https://github.com/Lullabot/sandbar/commit/d82bc02be124d6bdc45efaedfce9064e37755a44))
* drop SSH authorized_keys URL support ([2b0f40b](https://github.com/Lullabot/sandbar/commit/2b0f40b02ef93227af55b5e2778643e7e632144a))
* dynamically resolve user home directory instead of assuming /home/&lt;username&gt; ([#5](https://github.com/Lullabot/sandbar/issues/5)) ([fd7d4e2](https://github.com/Lullabot/sandbar/commit/fd7d4e25494f3810d4958bab7c81396cd82d21bf))
* genericize GitHub org setup with github-org-setup script ([#11](https://github.com/Lullabot/sandbar/issues/11)) ([86bdcf8](https://github.com/Lullabot/sandbar/commit/86bdcf81cd6f613dad20fc9efc14754441d13cf5))
* install gh CLI and configure git HTTPS auth via PAT ([#4](https://github.com/Lullabot/sandbar/issues/4)) ([cb0f708](https://github.com/Lullabot/sandbar/commit/cb0f708eeca9c6f4e3cf120c47c8d51c3ef614b4))
* install jq and yq by default ([#9](https://github.com/Lullabot/sandbar/issues/9)) ([763ae03](https://github.com/Lullabot/sandbar/commit/763ae03b6e97bd5cfb8a86b689774eda0c76bf75))
* install Node.js from NodeSource instead of Debian repositories ([#10](https://github.com/Lullabot/sandbar/issues/10)) ([cc48f89](https://github.com/Lullabot/sandbar/commit/cc48f896f83aeed9c92d13adec240cf845041629))
* make Docker registry proxy setup optional (off by default) ([#12](https://github.com/Lullabot/sandbar/issues/12)) ([0b7e675](https://github.com/Lullabot/sandbar/commit/0b7e67595e948848e4b1c1bec73f8b89c53e84c7))
* make primary system user configurable via `user_name` variable ([#2](https://github.com/Lullabot/sandbar/issues/2)) ([c59174b](https://github.com/Lullabot/sandbar/commit/c59174b8b7d8f3491b118d3d8eec02cab155188d))
* mark onboarding as completed ([4968973](https://github.com/Lullabot/sandbar/commit/4968973dae79658475b7577077ec3f380e03ceb4))
* **new-vm:** build a base image once and clone each VM from it ([29bc838](https://github.com/Lullabot/sandbar/commit/29bc838e9874c3b94669e23c6ad4f7c90719b09f))
* **new-vm:** guard against reusing an existing instance's baked config ([610ae4d](https://github.com/Lullabot/sandbar/commit/610ae4dd58d4643ce393e9f47fba661cfb538a1e))
* **new-vm:** prompt for disk size, registry proxy, and initial clone ([167ac32](https://github.com/Lullabot/sandbar/commit/167ac32bad607f1a000aef2746247a158eadf102))
* **new-vm:** provision once instead of on every boot ([51e6332](https://github.com/Lullabot/sandbar/commit/51e6332c319b4ffedfae17eaf4bb7b847644396d))
* **new-vm:** surface provisioning failures instead of failing silently ([a16bed7](https://github.com/Lullabot/sandbar/commit/a16bed780cc1f0ff686c6c7ec664253b3c9376d6))
* **playbook:** split provisioning into base/finalize phases ([0122371](https://github.com/Lullabot/sandbar/commit/0122371c1ef2f630bda5f282cd3539f6221a121b))
* provision CLAUDE_CODE_OAUTH_TOKEN for headless auth ([#15](https://github.com/Lullabot/sandbar/issues/15)) ([7824bda](https://github.com/Lullabot/sandbar/commit/7824bda6daf3423b712041111cd079ff88534226))
* **registry:** move data dir to sandbar with a migrate-then-cleanup ([3658fa5](https://github.com/Lullabot/sandbar/commit/3658fa516952d1bacf1f8b9f4675094f6070e07c))
* **release:** GoReleaser + Homebrew tap, and migrate CI to the sand binary ([5ce56e1](https://github.com/Lullabot/sandbar/commit/5ce56e156f9b87dd406d2d23d51cc392f982d962))
* remove Claude Code OAuth token support ([efe2f69](https://github.com/Lullabot/sandbar/commit/efe2f6990a88cd86c8364909c84edf3b3617cbaf))
* remove GitHub organization support ([3e6f37d](https://github.com/Lullabot/sandbar/commit/3e6f37d68545cc104c3be2529c1b0a11abf78b0f))
* remove webhook notification support ([c2f2df4](https://github.com/Lullabot/sandbar/commit/c2f2df4ad8d67c86d9cb2d58aeea06ce75f58298))
* **samba:** tune smb.conf for macOS clients via vfs_fruit ([#14](https://github.com/Lullabot/sandbar/issues/14)) ([6444a26](https://github.com/Lullabot/sandbar/commit/6444a26dc494f13017e53a0298d28e910d35e0a6))
* **sand:** drop no-op -y/-yes flags, document required flags ([a7e03da](https://github.com/Lullabot/sandbar/commit/a7e03daa83193714ca8ccb457b8bed409e530df2))
* **sand:** embed the playbook and add a headless `sand create` ([8354431](https://github.com/Lullabot/sandbar/commit/8354431ed72585300b582a93abf61ad8b7ef6a4f))
* **sand:** make --git-name/--git-email optional, fall back to host git config ([86019cb](https://github.com/Lullabot/sandbar/commit/86019cbc0f3a0f7b4422fbedd8b7d6e941222e88))
* **secrets:** apply on set + source env from profile and bashrc ([9344649](https://github.com/Lullabot/sandbar/commit/934464939969738a99a1eeb0bc801bfe9ec47fca))
* **secrets:** edit/delete in TUI, rm auto-applies, role purges on removal ([8d20a0a](https://github.com/Lullabot/sandbar/commit/8d20a0ac73f53f5a9fe6520e35ca34c03415a825))
* **secrets:** host secrets store + VM rendering role (plan-11 phase 1) ([8b11d10](https://github.com/Lullabot/sandbar/commit/8b11d10a02f646c3701af89d09a41ebf8757f42d))
* **secrets:** sand secret CLI + provisioning integration (plan-11 phase 2) ([a01a627](https://github.com/Lullabot/sandbar/commit/a01a6272479dd8723416d60eafb994aab3acde52))
* **secrets:** sand secret sync for live re-render (plan-11 phase 3) ([4f9293b](https://github.com/Lullabot/sandbar/commit/4f9293b9a58d5c571f645d1ed77d91bae6aa63c0))
* **secrets:** TUI secrets panel + gated e2e tests (plan-11 phase 4) ([06ce9d0](https://github.com/Lullabot/sandbar/commit/06ce9d01669c019df37acbd2bb8d1b1f84ae4be8))
* skip the Samba role in the Lima flow ([df22523](https://github.com/Lullabot/sandbar/commit/df22523462af9bb7932cfe3c8c0bed3d5a2b6770))
* **strikethroo:** right-size per-task model and effort tiers ([6c7b060](https://github.com/Lullabot/sandbar/commit/6c7b0604cac8e6f34804d56c2abef8cb12176284))
* **tui:** add Bubble Tea CRUD UI and claude-vm entry point ([a993f13](https://github.com/Lullabot/sandbar/commit/a993f13443cc32bd7092a5075bfb63e4255fadf8))
* **tui:** add file-transfer foundations (Copy, DirLister, dest prompt) ([5defe95](https://github.com/Lullabot/sandbar/commit/5defe95a03511b45160a5e37bf04dc5c69780181))
* **tui:** add limactl client and Runner abstraction ([cc813c2](https://github.com/Lullabot/sandbar/commit/cc813c260f9343e75b46492be0d880e47e4a7059))
* **tui:** add name search and disk-usage helper (plan 05 phase 1) ([1055db5](https://github.com/Lullabot/sandbar/commit/1055db58837ac3496a2567d2b2e21b07c24cfc22))
* **tui:** add Reset orchestration with optional preserve (phase 2) ([d868651](https://github.com/Lullabot/sandbar/commit/d86865181652a68ef8b779643f73ac10199e7dda))
* **tui:** add reusable file browser + gated copy e2e test ([b471f45](https://github.com/Lullabot/sandbar/commit/b471f450df02892969da53853162e2b678caf847))
* **tui:** cap default memory at half host RAM and warn on disk overflow ([6c1c910](https://github.com/Lullabot/sandbar/commit/6c1c9102ab37798b62b738b7107babdbf8435666))
* **tui:** create-form UX — field nav, info, GitHub labels; mark base image ([90a453c](https://github.com/Lullabot/sandbar/commit/90a453cbb7a7ed190123cbd6f090e617f2687559))
* **tui:** directory autocomplete for the transfer destination ([9862e2c](https://github.com/Lullabot/sandbar/commit/9862e2c37001421914f2e871f802bb2de0bb54c8))
* **tui:** guard destructive ops against non-managed VMs ([2d1e327](https://github.com/Lullabot/sandbar/commit/2d1e32705afa6697e1617f391b9e6c81d22b9cdc))
* **tui:** per-VM sizing + staging helpers for VM reset (phase 1) ([003bc09](https://github.com/Lullabot/sandbar/commit/003bc092d51966110591d323ecfcc40b889d80a9))
* **tui:** port base/clone/finalize orchestration to Go ([23f85b8](https://github.com/Lullabot/sandbar/commit/23f85b82574993f9d6c0c4d57a832609a306f580))
* **tui:** relabel Disk as Max Disk and add real Disk Used column (plan 05 phase 2) ([ccebec5](https://github.com/Lullabot/sandbar/commit/ccebec507e80cbbbfabd1ba272fc54bf936ecb66))
* **tui:** reset flow — pre-filled form, preserve toggles (phase 3) ([b7c71b4](https://github.com/Lullabot/sandbar/commit/b7c71b402c965a855711b945a7874e12ae59428a))
* **tui:** scaffold Go module and VM domain model ([e142765](https://github.com/Lullabot/sandbar/commit/e14276589f145927c16befc495d318ab60b0acf3))
* **tui:** shell into a VM (S) and drop the unused IP field ([d940f01](https://github.com/Lullabot/sandbar/commit/d940f0166dfcebbdd54ef4fd0a5b68fc96282870))
* **tui:** show a spinner while VM lifecycle actions run ([1b10668](https://github.com/Lullabot/sandbar/commit/1b106684fd4fde5ad3ce200a3805c33d3dd026e1))
* **tui:** show memory/disk as human sizes (GiB) in list and detail ([a13d535](https://github.com/Lullabot/sandbar/commit/a13d535118999825e7085cd475baa3b81a2c74d7))
* **tui:** stream VM lifecycle steps and cancel builds with ctrl+c ([2d067d7](https://github.com/Lullabot/sandbar/commit/2d067d70a49046110b35bae388968abb2d015b65))
* **tui:** wire Upload/Download actions and the transfer flow ([b6975b7](https://github.com/Lullabot/sandbar/commit/b6975b7ed9e099a477ef24d28cb1403b47c89d86))


### Bug Fixes

* add ~/.local/bin to PATH in ~/.bashrc after Claude Code install ([#7](https://github.com/Lullabot/sandbar/issues/7)) ([cc8b241](https://github.com/Lullabot/sandbar/commit/cc8b24108bf16f4f3107e9346e8b921894031557))
* add acl package, sort ([6a65a7f](https://github.com/Lullabot/sandbar/commit/6a65a7f4c2fa25155afbae08adf863e7cb368872))
* any-version is now any in cloudflare repo ([94a4766](https://github.com/Lullabot/sandbar/commit/94a4766e9619b7570775f2779266ae04215b6816))
* **ci:** drop removed --yes flag from lima-e2e sand create ([2b4d8c3](https://github.com/Lullabot/sandbar/commit/2b4d8c317633f9b1eb7c02ae08b67536fbc970d7))
* **deps:** update module github.com/charmbracelet/x/ansi to v0.11.7 ([#13](https://github.com/Lullabot/sandbar/issues/13)) ([e86c738](https://github.com/Lullabot/sandbar/commit/e86c738070f6f1f36ed8d39078620f86bd182d92))
* **deps:** update module golang.org/x/sys to v0.47.0 ([#16](https://github.com/Lullabot/sandbar/issues/16)) ([ebb425e](https://github.com/Lullabot/sandbar/commit/ebb425eea01ad37940f32d385b6871edb28e6c8b))
* install gh CLI before configuring GitHub PAT ([#6](https://github.com/Lullabot/sandbar/issues/6)) ([b97388c](https://github.com/Lullabot/sandbar/commit/b97388c3b7cf9e51924c07382c1f4eb7e09a7566))
* **install:** honor --ref in the curl | bash path ([b6fb10d](https://github.com/Lullabot/sandbar/commit/b6fb10da742f9112e70de5d907a38cf54fd8b431))
* **new-vm:** clear error when a flag is missing its value ([1d10815](https://github.com/Lullabot/sandbar/commit/1d10815d4595234248e472961f805ef8b7209fcf))
* **new-vm:** correct ordering of the final summary banner ([3794bb2](https://github.com/Lullabot/sandbar/commit/3794bb2b75f7a7a357189751350bd718c94d01be))
* **new-vm:** don't break apt keyrings with a global umask ([6a4ca82](https://github.com/Lullabot/sandbar/commit/6a4ca8249137c7e518cae4a724abc15e4f151466))
* **new-vm:** make --recreate discoverable over curl|bash ([08acb67](https://github.com/Lullabot/sandbar/commit/08acb6751c2e2254af0e963a4438448a64ff82de))
* **new-vm:** restart the VM after first provision ([a884224](https://github.com/Lullabot/sandbar/commit/a8842246473582001e0ce38bc74dc75b9a428361))
* **new-vm:** simplify language ([fd78c73](https://github.com/Lullabot/sandbar/commit/fd78c73f5b149b7e3445fc4a4787e00c3ec86b34))
* **new-vm:** stop final-message backticks running as commands ([49f52f5](https://github.com/Lullabot/sandbar/commit/49f52f5c3e05acf7a4b918a3d6a0e83d8849b652))
* **new-vm:** validate cpus is a positive integer ([ceeda8c](https://github.com/Lullabot/sandbar/commit/ceeda8c432200cd5b50e11ca76364247019a6ba9))
* prevent tmux from overriding SSH_AUTH_SOCK symlink on attach ([#1](https://github.com/Lullabot/sandbar/issues/1)) ([39af6be](https://github.com/Lullabot/sandbar/commit/39af6be0738a57fb43bdf15b43560f957494787c))
* **project:** strip trailing slash from clone URL before deriving paths ([c7bad77](https://github.com/Lullabot/sandbar/commit/c7bad770926575db8097c38910db3bc6fbe00d46))
* **provision:** sync only the playbook fileset into the guest ([8f6c7ee](https://github.com/Lullabot/sandbar/commit/8f6c7ee697006757363a4568eaece725b096c070))
* replace PII and internal hostnames with placeholders ([0aba978](https://github.com/Lullabot/sandbar/commit/0aba978ff6257d06a8a92df2bf896e4e8fbf8d4e))
* **sand:** default headless create's user to the host username ([9cafced](https://github.com/Lullabot/sandbar/commit/9cafceda2f56842a2a6ff096c4255d48306dc603))
* **secrets:** source global env from ~/.profile; capture guest stdout cleanly ([1e88166](https://github.com/Lullabot/sandbar/commit/1e88166ef67c61df287ddb6c190601ba26c74fcd))
* **tui:** clear pending selection on back-out; keep browser navigable on load error ([3d6d2ee](https://github.com/Lullabot/sandbar/commit/3d6d2eed41f391bf9ba3803459e41fb6dd949963))
* **tui:** clear the browser filter on navigate and select ([92a3215](https://github.com/Lullabot/sandbar/commit/92a3215377a0a76cb10b2608ea0d6d5a14a6950b))
* **tui:** compact the file browser and return to the VM after a transfer ([178f604](https://github.com/Lullabot/sandbar/commit/178f6041c4517dfeee11b5750734a9bad10b56b5))
* **tui:** don't merge limactl stderr into parsed stdout ([e17647e](https://github.com/Lullabot/sandbar/commit/e17647e85c8e6c24c3920aed449c0e1433445dd6))
* **tui:** don't reset the dest autocomplete selection on cursor blink ([25235b1](https://github.com/Lullabot/sandbar/commit/25235b136a7ffe0673a364de4d9f7041c00c57db))
* **tui:** let backspace edit form fields instead of navigating back ([6058f0c](https://github.com/Lullabot/sandbar/commit/6058f0ce745ceb6f7daf53e895e8af7d54f4a570))
* **tui:** nest a directory transfer under the destination ([49afb8d](https://github.com/Lullabot/sandbar/commit/49afb8db14583cee4ed5f56a90edc3987293232d))
* **tui:** read the guest home from cloud-config.yaml, not /home/&lt;user&gt; ([e1bbe5b](https://github.com/Lullabot/sandbar/commit/e1bbe5b5e4f27da9a6ee0b57a29a8a8fe7ec386f))
* **tui:** resolve the guest home from ssh.config, not the host username ([a252fad](https://github.com/Lullabot/sandbar/commit/a252fad967f531bf6fab27a3006721b629e35c7c))
* **tui:** seed create-form defaults and default blank fields like new-vm.sh ([f2a308e](https://github.com/Lullabot/sandbar/commit/f2a308e552db8dca01abf5bdb5e89e244eabde2d))
* **tui:** surface committed search filter and move disk stat off the Update loop ([299f787](https://github.com/Lullabot/sandbar/commit/299f7872c1adbad3f386643dae6815f2241d790b))
* **tui:** wrap streamed provision output to the viewport width ([8c0f2f3](https://github.com/Lullabot/sandbar/commit/8c0f2f35d2e4488c7de938d4cfbeb57a186536bb))
* **user:** restore optional SSH authorized_keys for non-Lima users ([d81b59e](https://github.com/Lullabot/sandbar/commit/d81b59e42acb15ba33ee33e316cfbb45aac50849))
* wrong shell for claude code install ([c5f83c5](https://github.com/Lullabot/sandbar/commit/c5f83c591474d6dcc5524198a81336312af96dd1))

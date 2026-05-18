type CommandGroup = {
	title: string;
	commands: { syntax: string; description?: string }[];
};

const COMMON_COMMANDS: CommandGroup[] = [
	{
		title: "General",
		commands: [
			{ syntax: "help", description: "Show this help" },
			{ syntax: "clear", description: "Clear terminal output" },
			{ syntax: "history", description: "Show command history" },
			{ syntax: "freeze", description: "Pause the node (suspend process)" },
			{ syntax: "unfreeze", description: "Resume a frozen node" },
		],
	},
	{
		title: "Interfaces",
		commands: [
			{ syntax: "ip addr", description: "Show interface addresses and states" },
			{ syntax: "ip set [interface] [ip/prefix]", description: "Assign IP address to interface" },
			{ syntax: "ip unset [interface]", description: "Remove IP address from interface" },
			{ syntax: "ip link set [interface] [up|down]", description: "Bring interface up or down" },
		],
	},
	{
		title: "Cable flapping",
		commands: [
			{ syntax: "ip flap start [interface] [--down ms] [--up ms] [--jitter ms]", description: "Start interface flapping (alternating up/down)" },
			{ syntax: "ip flap stop [interface]", description: "Stop interface flapping" },
			{ syntax: "ip flap status [interface]", description: "Show flap status" },
		],
	},
	{
		title: "Routing",
		commands: [
			{ syntax: "ip route", description: "Show routing table" },
			{ syntax: "ip route default [next-hop]", description: "Set default gateway" },
			{ syntax: "ip route add [destination/prefix] via [next-hop]", description: "Add a static route" },
			{ syntax: "ip route blackhole [destination/prefix]", description: "Add a blackhole route" },
			{ syntax: "ip route delete [default|destination/prefix]", description: "Delete a route" },
		],
	},
	{
		title: "Connectivity",
		commands: [
			{ syntax: "ping [target-ip] [--count packets]", description: "Ping a host" },
			{ syntax: "traceroute [target-ip] [--max-hops count(1..255)]", description: "Trace route to host" },
		],
	},
	{
		title: "iperf",
		commands: [
			{ syntax: "iperf tcp [ip] [--time seconds | --bytes bytes]", description: "Run TCP throughput test" },
			{ syntax: "iperf udp [ip] [--time seconds | --bytes bytes] [--bitrate rate[K|M|G]] [--packet-length bytes(16..65507)]", description: "Run UDP throughput test" },
			{ syntax: "iperf server start", description: "Start iperf server" },
			{ syntax: "iperf server stop", description: "Stop iperf server" },
			{ syntax: "iperf server status", description: "Show iperf server status" },
			{ syntax: "iperf server log", description: "Show iperf server log" },
			{ syntax: "iperf server log clear", description: "Clear iperf server log" },
		],
	},
	{
		title: "HTTP",
		commands: [
			{ syntax: "http get [ip]", description: "Send HTTP GET request" },
			{ syntax: "http server start", description: "Start HTTP server" },
			{ syntax: "http server stop", description: "Stop HTTP server" },
			{ syntax: "http server status", description: "Show HTTP server status" },
			{ syntax: "http server log", description: "Show HTTP server log" },
			{ syntax: "http server log clear", description: "Clear HTTP server log" },
		],
	},
	{
		title: "TCP / UDP",
		commands: [
			{ syntax: "tcp server start", description: "Start TCP echo server" },
			{ syntax: "tcp server stop", description: "Stop TCP echo server" },
			{ syntax: "tcp server status", description: "Show TCP server status" },
			{ syntax: "tcp connect [ip]", description: "Connect to TCP server" },
			{ syntax: "udp server start", description: "Start UDP echo server" },
			{ syntax: "udp server stop", description: "Stop UDP echo server" },
			{ syntax: "udp server status", description: "Show UDP server status" },
			{ syntax: "udp probe [ip]", description: "Send UDP probe" },
		],
	},
	{
		title: "Traffic control (tc)",
		commands: [
			{ syntax: "tc show [interface]", description: "Show tc rules on interface" },
			{ syntax: "tc clear [interface]", description: "Remove all tc rules from interface" },
			{
				syntax: "tc set [interface] [--delay ms] [--jitter ms] [--loss pct] [--loss-correlation pct] [--reorder pct] [--duplicate pct] [--corrupt pct] [--bandwidth kbit] [--queue-limit packets]",
				description: "Configure traffic control on interface",
			},
		],
	},
];

const NOTE_SWITCH = "Switches do not support: ip addr, ip set/unset, ip route, ping, traceroute, iperf, http, tcp, udp";

export function HelpPage() {
	return (
		<div className="min-h-screen bg-white text-zinc-900">
			<main className="mx-auto max-w-4xl px-8 py-10">
				<h1 className="mb-1 text-2xl font-bold text-zinc-900">Command Reference</h1>
				<p className="mb-8 text-sm text-zinc-500">
					Commands available in the node terminal. Type <code className="rounded bg-zinc-100 px-1.5 py-0.5 font-mono text-zinc-700">help</code> in any terminal to see this list inline.
				</p>

				{/* Switch note */}
				<div className="mb-8 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800">
					<span className="font-medium">Note: </span>{NOTE_SWITCH}
				</div>

				<div className="flex flex-col gap-8">
					{COMMON_COMMANDS.map((group) => (
						<section key={group.title}>
							<h2 className="mb-3 text-xs font-semibold uppercase tracking-wider text-zinc-400">
								{group.title}
							</h2>
							<div className="overflow-hidden rounded-lg border border-zinc-200">
								<table className="w-full text-sm">
									<tbody>
										{group.commands.map((cmd, i) => (
											<tr
												key={i}
												className={i % 2 === 0 ? "bg-white" : "bg-zinc-50"}
											>
												<td className="px-4 py-2.5 align-top font-mono text-[12px] text-zinc-800 w-[55%]">
													{cmd.syntax}
												</td>
												<td className="px-4 py-2.5 align-top text-[12px] text-zinc-500">
													{cmd.description ?? ""}
												</td>
											</tr>
										))}
									</tbody>
								</table>
							</div>
						</section>
					))}
				</div>
			</main>
		</div>
	);
}

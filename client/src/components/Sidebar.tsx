import {
	Activity,
	AlertCircle,
	ArrowDown,
	ArrowRight,
	ArrowUp,
	Ban,
	CheckCircle,
	ChevronDown,
	Snowflake,
	Loader2,
	Monitor,
	Network,
	PencilLine,
	Play,
	Route,
	Router as RouterIcon,
	ScrollText,
	Server,
	Settings2,
	Square,
	RotateCcw,
	Trash2,
	X,
	Terminal,
} from "lucide-react";
import { useEffect, useState } from "react";
import type { Edge, Node } from "reactflow";
import type { InterfaceLabelEdgeData } from "./InterfaceLabelEdge";
import type { SquareNodeData } from "./SquareNode";
import type { ApiInterface } from "../services/topology";

export type SidebarLastCommand = {
	command: string;
	status: "idle" | "executing" | "done" | "success" | "failed";
	errorMessage?: string;
	nodeId?: string;
};

type SidebarProps = {
	selectedNode: Node<SquareNodeData> | null;
	selectedEdge: Edge<InterfaceLabelEdgeData> | null;
	nodes: Node<SquareNodeData>[];
	edges?: Edge<InterfaceLabelEdgeData>[];
	isBusy: boolean;
	isCollapsed: boolean;
	recentCommands: string[];
	lastCommand: SidebarLastCommand | null;
	onRenameNode: (nodeId: string, displayName: string) => void;
	onOpenTerminal: (nodeId: string) => void;
	onToggleRun: (nodeId: string) => void;
	onToggleFreeze: (nodeId: string) => void;
	onSetInterfaceAddress: (nodeId: string, interfaceName: string, cidr: string) => void;
	onUnsetInterfaceAddress: (nodeId: string, interfaceName: string) => void;
	onSetInterfaceAdminState: (nodeId: string, interfaceName: string, up: boolean) => void;
	onSetInterfaceFlap: (
		nodeId: string,
		interfaceName: string,
		enabled: boolean,
		options: { downMs: number; upMs: number; jitterMs: number },
	) => void;
	onSetInterfaceTC: (
		nodeId: string,
		interfaceName: string,
		options: {
			delayMs: number;
			jitterMs: number;
			lossPct: number;
			lossCorrelationPct: number;
			reorderPct: number;
			duplicatePct: number;
			corruptPct: number;
			bandwidthKbit: number;
			queueLimitPackets: number;
		},
	) => void;
	onClearInterfaceTC: (nodeId: string, interfaceName: string) => void;
	onListRoutes: (nodeId: string) => Promise<RouteRule[]>;
	onAddRoute: (nodeId: string, destination: string, gatewayOrBlackhole: string) => Promise<boolean>;
	onDeleteRoute: (nodeId: string, route: RouteRule) => Promise<void>;
	onExecuteNodeCommand: (nodeId: string, command: string, options?: { silent?: boolean }) => Promise<boolean>;
	onRecordFailedNodeCommand: (nodeId: string, command: string, errorMessage: string) => void;
	onClearRecentCommands: (nodeId: string) => void;
	onRequestDeleteNode: () => void;
	onDeleteLink: () => void;
	onToggleCollapse: () => void;
	nodeSidebarStateByNodeId: Record<string, NodeSidebarViewState>;
	onNodeSidebarStateChange: (nodeId: string, next: NodeSidebarViewState) => void;
};

export type NodeSidebarViewState = {
	interfacesCollapsed: boolean;
	routesCollapsed: boolean;
	serversCollapsed: boolean;
	sendDataCollapsed: boolean;
	recentCollapsed: boolean;
	actionsCollapsed: boolean;
};

type RouteRule = {
	destination: string;
	nextHop: string;
	kind: "via" | "blackhole";
};

type ServerName = "iperf" | "http" | "tcp" | "udp";
type ServerStatus = "stopped" | "running" | "loading";

function NodeIcon({ type, className }: { type: string; className?: string }) {
	const cls = className ?? "w-5 h-5 text-gray-600";
	if (type === "router") return <RouterIcon className={cls} />;
	if (type === "switch") return <Network className={cls} />;
	return <Monitor className={cls} />;
}

const TYPE_LABEL: Record<string, string> = {
	host: "Endpoint device",
	switch: "Network switch",
	router: "Network router",
};

function getInterfaceStateLabel(state: "connected" | "disconnected") {
	return state === "connected" ? "Connected" : "Disconnected";
}

function getInterfaceStateColor(state: "connected" | "disconnected") {
	return state === "connected" ? "text-green-600" : "text-gray-400";
}

function isValidIPv4(value: string): boolean {
	const parts = value.trim().split(".");
	if (parts.length !== 4) return false;
	for (const part of parts) {
		if (!/^\d+$/.test(part)) return false;
		const n = Number(part);
		if (!Number.isInteger(n) || n < 0 || n > 255) return false;
	}
	return true;
}

function parsePositiveInt(value: string): number | null {
	if (!/^\d+$/.test(value.trim())) return null;
	const parsed = Number(value);
	if (!Number.isInteger(parsed) || parsed <= 0) return null;
	return parsed;
}

function InterfaceCard({
	nodeId,
	iface,
	isBusy,
	canControl,
	onSetInterfaceAddress,
	onUnsetInterfaceAddress,
	onSetInterfaceAdminState,
	onSetInterfaceFlap,
	onSetInterfaceTC,
	onClearInterfaceTC,
}: {
	nodeId: string;
	iface: ApiInterface;
	isBusy: boolean;
	canControl: boolean;
	onSetInterfaceAddress: (nodeId: string, interfaceName: string, cidr: string) => void;
	onUnsetInterfaceAddress: (nodeId: string, interfaceName: string) => void;
	onSetInterfaceAdminState: (nodeId: string, interfaceName: string, up: boolean) => void;
	onSetInterfaceFlap: (
		nodeId: string,
		interfaceName: string,
		enabled: boolean,
		options: { downMs: number; upMs: number; jitterMs: number },
	) => void;
	onSetInterfaceTC: (
		nodeId: string,
		interfaceName: string,
		options: {
			delayMs: number;
			jitterMs: number;
			lossPct: number;
			lossCorrelationPct: number;
			reorderPct: number;
			duplicatePct: number;
			corruptPct: number;
			bandwidthKbit: number;
			queueLimitPackets: number;
		},
	) => void;
	onClearInterfaceTC: (nodeId: string, interfaceName: string) => void;
}) {
	const isLinked = iface.linkId !== "";
	const hasIp = iface.ipAddress !== "" && iface.prefixLength > 0;
	const cidr = hasIp ? `${iface.ipAddress}/${iface.prefixLength}` : "";
	const adminUp = !iface.adminDown;
	const flap = iface.flap ?? { enabled: false, downMs: 0, upMs: 0, jitterMs: 0 };
	const flapping = flap.enabled;
	const nicDown = !adminUp;
	const defaultFlap = {
		downMs: flap.downMs > 0 ? flap.downMs : 1000,
		upMs: flap.upMs > 0 ? flap.upMs : 1000,
		jitterMs: flap.jitterMs >= 0 ? flap.jitterMs : 200,
	};
	const [editingIP, setEditingIP] = useState(false);
	const [controlsCollapsed, setControlsCollapsed] = useState(true);
	const [draftIP, setDraftIP] = useState(cidr);
	const [flapDraft, setFlapDraft] = useState({
		downMs: String(defaultFlap.downMs),
		upMs: String(defaultFlap.upMs),
		jitterMs: String(defaultFlap.jitterMs),
	});
	const currentTC = iface.conditions ?? {
		delayMs: 0,
		jitterMs: 0,
		lossPct: 0,
		lossCorrelationPct: 0,
		reorderPct: 0,
		duplicatePct: 0,
		corruptPct: 0,
		bandwidthKbit: 0,
		queueLimitPackets: 0,
	};
	const [tcDraft, setTcDraft] = useState({
		delayMs: String(currentTC.delayMs),
		jitterMs: String(currentTC.jitterMs),
		lossPct: String(currentTC.lossPct),
		lossCorrelationPct: String(currentTC.lossCorrelationPct),
		reorderPct: String(currentTC.reorderPct),
		duplicatePct: String(currentTC.duplicatePct),
		corruptPct: String(currentTC.corruptPct),
		bandwidthKbit: String(currentTC.bandwidthKbit),
		queueLimitPackets: String(currentTC.queueLimitPackets),
	});

	useEffect(() => {
		if (!editingIP) {
			setDraftIP(cidr);
		}
	}, [cidr, editingIP]);
	useEffect(() => {
		if (canControl) return;
		setEditingIP(false);
	}, [canControl]);

	useEffect(() => {
		setFlapDraft({
			downMs: String(defaultFlap.downMs),
			upMs: String(defaultFlap.upMs),
			jitterMs: String(defaultFlap.jitterMs),
		});
	}, [defaultFlap.downMs, defaultFlap.upMs, defaultFlap.jitterMs]);
	useEffect(() => {
		setTcDraft({
			delayMs: String(currentTC.delayMs),
			jitterMs: String(currentTC.jitterMs),
			lossPct: String(currentTC.lossPct),
			lossCorrelationPct: String(currentTC.lossCorrelationPct),
			reorderPct: String(currentTC.reorderPct),
			duplicatePct: String(currentTC.duplicatePct),
			corruptPct: String(currentTC.corruptPct),
			bandwidthKbit: String(currentTC.bandwidthKbit),
			queueLimitPackets: String(currentTC.queueLimitPackets),
		});
	}, [
		currentTC.bandwidthKbit,
		currentTC.corruptPct,
		currentTC.delayMs,
		currentTC.duplicatePct,
		currentTC.jitterMs,
		currentTC.lossCorrelationPct,
		currentTC.lossPct,
		currentTC.queueLimitPackets,
		currentTC.reorderPct,
	]);

	const submitIP = () => {
		if (!canControl) {
			setEditingIP(false);
			return;
		}
		const next = draftIP.trim();
		setEditingIP(false);
		if (next === cidr) return;
		if (next === "") {
			onUnsetInterfaceAddress(nodeId, iface.name);
			return;
		}
		onSetInterfaceAddress(nodeId, iface.name, next);
	};

	const normalizedFlap = {
		downMs: Math.max(0, Math.floor(Number(flapDraft.downMs || "0"))),
		upMs: Math.max(0, Math.floor(Number(flapDraft.upMs || "0"))),
		jitterMs: Math.max(0, Math.floor(Number(flapDraft.jitterMs || "0"))),
	};
	const normalizedTC = {
		delayMs: Math.max(0, Math.floor(Number(tcDraft.delayMs || "0"))),
		jitterMs: Math.max(0, Math.floor(Number(tcDraft.jitterMs || "0"))),
		lossPct: Math.max(0, Number(tcDraft.lossPct || "0")),
		lossCorrelationPct: Math.max(0, Number(tcDraft.lossCorrelationPct || "0")),
		reorderPct: Math.max(0, Number(tcDraft.reorderPct || "0")),
		duplicatePct: Math.max(0, Number(tcDraft.duplicatePct || "0")),
		corruptPct: Math.max(0, Number(tcDraft.corruptPct || "0")),
		bandwidthKbit: Math.max(0, Math.floor(Number(tcDraft.bandwidthKbit || "0"))),
		queueLimitPackets: Math.max(0, Math.floor(Number(tcDraft.queueLimitPackets || "0"))),
	};
	const tcEnabled = currentTC.delayMs > 0
		|| currentTC.jitterMs > 0
		|| currentTC.lossPct > 0
		|| currentTC.lossCorrelationPct > 0
		|| currentTC.reorderPct > 0
		|| currentTC.duplicatePct > 0
		|| currentTC.corruptPct > 0
		|| currentTC.bandwidthKbit > 0
		|| currentTC.queueLimitPackets > 0;

	return (
		<div className={`rounded-lg border bg-gray-50 p-3 transition-colors ${nicDown ? "border-red-300" : "border-gray-200"}`}>
			<div className="mb-2 flex items-center justify-between gap-2">
				<div className="flex items-center gap-2">
					<div className={`h-2 w-2 rounded-full ${isLinked ? "bg-green-500" : "bg-gray-400"}`} />
					<span className="text-sm font-medium text-gray-900">{iface.name}</span>
				</div>
				<span className={`text-xs ${getInterfaceStateColor(isLinked ? "connected" : "disconnected")}`}>
					{getInterfaceStateLabel(isLinked ? "connected" : "disconnected")}
				</span>
			</div>

			<div className="mb-2">
				<div className="relative h-7 w-full">
					{editingIP ? (
						<input
							value={draftIP}
							onChange={(e) => setDraftIP(e.target.value)}
							onBlur={submitIP}
							onKeyDown={(e) => {
								if (e.key === "Enter") submitIP();
								if (e.key === "Escape") {
									setEditingIP(false);
									setDraftIP(cidr);
								}
							}}
							disabled={isBusy || !canControl}
							autoFocus
							placeholder="IP unset"
							className="absolute inset-0 h-7 w-full rounded-md border border-blue-200 bg-blue-50 px-2 text-sm text-gray-900 outline-none focus:border-blue-400 focus:bg-white disabled:opacity-60"
						/>
					) : (
						<button
							type="button"
							onClick={() => setEditingIP(true)}
							disabled={isBusy || !canControl}
							className="absolute inset-0 flex h-7 w-full items-center gap-1.5 rounded-md border border-gray-200 bg-white px-2 text-left text-sm text-gray-700 transition-colors hover:bg-gray-50 disabled:opacity-60 disabled:cursor-default"
						>
							<span className={`min-w-0 flex-1 truncate ${hasIp ? "text-gray-800" : "text-gray-400"}`}>{hasIp ? cidr : "IP unset"}</span>
							{canControl && !isBusy ? <PencilLine className="h-3.5 w-3.5 shrink-0 text-gray-400" /> : null}
						</button>
					)}
				</div>
			</div>

			{canControl ? (
				<div className="mt-2 rounded-md border border-gray-200 bg-white p-2">
					<div className={`flex min-h-[32px] items-center gap-2 ${controlsCollapsed ? "" : "mb-2"}`}>
						<button
							type="button"
							onClick={() => setControlsCollapsed((v) => !v)}
							className="flex h-7 w-7 items-center justify-center rounded border border-gray-200 bg-white text-gray-600 transition-colors hover:bg-gray-50"
							aria-label={controlsCollapsed ? "Show controls" : "Hide controls"}
						>
							<Settings2 className="h-3.5 w-3.5" />
						</button>
						<button
							type="button"
							onClick={() => onSetInterfaceAdminState(nodeId, iface.name, iface.adminDown)}
							disabled={isBusy}
							className={`relative h-8 flex-1 rounded-md border px-1 transition-colors disabled:opacity-60 disabled:cursor-default ${isBusy ? "border-yellow-300 bg-yellow-50" : adminUp ? "border-green-300 bg-green-50" : "border-red-300 bg-red-50"
								}`}
						>
							<div className="relative z-[1] h-full w-full text-xs font-semibold text-gray-700">
								<span className="absolute left-[25%] top-1/2 -translate-x-1/2 -translate-y-1/2">Up</span>
								<span className="absolute left-[75%] top-1/2 -translate-x-1/2 -translate-y-1/2">Down</span>
							</div>
							<div className={`absolute top-1/2 h-6 w-[49%] -translate-y-1/2 rounded-md bg-white shadow-sm transition-transform duration-200 ease-in-out ${adminUp ? "translate-x-0.5" : "translate-x-[98%]"}`}>
								<div className="h-full w-full" />
							</div>
						</button>
					</div>

					<div className={`overflow-hidden transition-all duration-200 ${controlsCollapsed ? "max-h-0 opacity-0 -translate-y-1" : "max-h-[520px] opacity-100 translate-y-0"}`}>

						<div className="mt-2 rounded-md border border-sky-200 bg-sky-50/70 p-1.5">
							<div className="mb-1 flex items-center justify-between gap-2">
								<button
									type="button"
									onClick={() => onSetInterfaceFlap(nodeId, iface.name, !flapping, normalizedFlap)}
									disabled={isBusy}
									className={`flex h-7 items-center gap-1 rounded-md border px-2 text-xs transition-colors disabled:opacity-60 disabled:cursor-default ${flapping ? "border-sky-300 bg-sky-100 text-sky-800 hover:bg-sky-200" : "border-sky-200 bg-white text-sky-700 hover:bg-sky-100"
										}`}
								>
									<Activity className={`h-3.5 w-3.5 ${flapping ? "animate-[pulse_1.8s_ease-in-out_infinite]" : ""}`} />
									Flap
								</button>
								<button
									type="button"
									onClick={() => onSetInterfaceFlap(nodeId, iface.name, true, normalizedFlap)}
									disabled={isBusy || !flapping}
									className="h-7 rounded-md border border-sky-200 bg-white px-2 text-xs text-sky-800 transition-colors hover:bg-sky-100 disabled:opacity-50 disabled:cursor-default"
								>
									Apply
								</button>
							</div>
							<div className="mt-1 flex flex-wrap items-center justify-between gap-2 px-0.5 py-0.5">
								<div className="flex items-center gap-1" title="Down ms">
									<ArrowDown className="h-3 w-3 text-gray-500" />
									<input type="text" inputMode="numeric" pattern="[0-9]*" value={flapDraft.downMs} onChange={(e) => setFlapDraft((curr) => ({ ...curr, downMs: e.target.value.replace(/\D/g, "") }))} disabled={isBusy} className="h-6 w-10 rounded border border-gray-200 bg-white px-1 text-[11px] text-gray-700 outline-none focus:border-sky-400 disabled:opacity-60" />
								</div>
								<div className="flex items-center gap-1" title="Up ms">
									<ArrowUp className="h-3 w-3 text-gray-500" />
									<input type="text" inputMode="numeric" pattern="[0-9]*" value={flapDraft.upMs} onChange={(e) => setFlapDraft((curr) => ({ ...curr, upMs: e.target.value.replace(/\D/g, "") }))} disabled={isBusy} className="h-6 w-10 rounded border border-gray-200 bg-white px-1 text-[11px] text-gray-700 outline-none focus:border-sky-400 disabled:opacity-60" />
								</div>
								<div className="flex items-center gap-1" title="Jitter ms">
									<Activity className="h-3 w-3 text-gray-500" />
									<input type="text" inputMode="numeric" pattern="[0-9]*" value={flapDraft.jitterMs} onChange={(e) => setFlapDraft((curr) => ({ ...curr, jitterMs: e.target.value.replace(/\D/g, "") }))} disabled={isBusy} className="h-6 w-10 rounded border border-gray-200 bg-white px-1 text-[11px] text-gray-700 outline-none focus:border-sky-400 disabled:opacity-60" />
								</div>
							</div>
						</div>

						<div className="mt-2 rounded-md border border-gray-200 bg-white p-2">
							<div className="mb-1 flex items-center justify-between">
								<span className="text-[11px] font-medium text-gray-600">Traffic Control</span>
								{tcEnabled ? <span className="text-[11px] text-gray-500">Configured</span> : null}
							</div>
							<div className="grid grid-cols-3 gap-1.5">
								<div className="flex items-center gap-1" title="Delay ms">
									<span className="w-8 text-[10px] text-gray-500">Dly</span>
									<input type="text" inputMode="numeric" value={tcDraft.delayMs} onChange={(e) => setTcDraft((c) => ({ ...c, delayMs: e.target.value.replace(/\D/g, "") }))} disabled={isBusy} className="h-6 w-full rounded border border-gray-200 px-1 text-[11px] outline-none focus:border-sky-400 disabled:opacity-60" />
								</div>
								<div className="flex items-center gap-1" title="Jitter ms">
									<span className="w-8 text-[10px] text-gray-500">Jit</span>
									<input type="text" inputMode="numeric" value={tcDraft.jitterMs} onChange={(e) => setTcDraft((c) => ({ ...c, jitterMs: e.target.value.replace(/\D/g, "") }))} disabled={isBusy} className="h-6 w-full rounded border border-gray-200 px-1 text-[11px] outline-none focus:border-sky-400 disabled:opacity-60" />
								</div>
								<div className="flex items-center gap-1" title="Bandwidth kbit">
									<span className="w-8 text-[10px] text-gray-500">Bw</span>
									<input type="text" inputMode="numeric" value={tcDraft.bandwidthKbit} onChange={(e) => setTcDraft((c) => ({ ...c, bandwidthKbit: e.target.value.replace(/\D/g, "") }))} disabled={isBusy} className="h-6 w-full rounded border border-gray-200 px-1 text-[11px] outline-none focus:border-sky-400 disabled:opacity-60" />
								</div>
								<div className="flex items-center gap-1" title="Loss %">
									<span className="w-8 text-[10px] text-gray-500">Loss</span>
									<input type="text" inputMode="decimal" value={tcDraft.lossPct} onChange={(e) => setTcDraft((c) => ({ ...c, lossPct: e.target.value.replace(/[^0-9.]/g, "") }))} disabled={isBusy} className="h-6 w-full rounded border border-gray-200 px-1 text-[11px] outline-none focus:border-sky-400 disabled:opacity-60" />
								</div>
								<div className="flex items-center gap-1" title="Loss correlation %">
									<span className="w-8 text-[10px] text-gray-500">LCor</span>
									<input type="text" inputMode="decimal" value={tcDraft.lossCorrelationPct} onChange={(e) => setTcDraft((c) => ({ ...c, lossCorrelationPct: e.target.value.replace(/[^0-9.]/g, "") }))} disabled={isBusy} className="h-6 w-full rounded border border-gray-200 px-1 text-[11px] outline-none focus:border-sky-400 disabled:opacity-60" />
								</div>
								<div className="flex items-center gap-1" title="Queue limit packets">
									<span className="w-8 text-[10px] text-gray-500">Q</span>
									<input type="text" inputMode="numeric" value={tcDraft.queueLimitPackets} onChange={(e) => setTcDraft((c) => ({ ...c, queueLimitPackets: e.target.value.replace(/\D/g, "") }))} disabled={isBusy} className="h-6 w-full rounded border border-gray-200 px-1 text-[11px] outline-none focus:border-sky-400 disabled:opacity-60" />
								</div>
								<div className="flex items-center gap-1" title="Reorder %">
									<span className="w-8 text-[10px] text-gray-500">Reo</span>
									<input type="text" inputMode="decimal" value={tcDraft.reorderPct} onChange={(e) => setTcDraft((c) => ({ ...c, reorderPct: e.target.value.replace(/[^0-9.]/g, "") }))} disabled={isBusy} className="h-6 w-full rounded border border-gray-200 px-1 text-[11px] outline-none focus:border-sky-400 disabled:opacity-60" />
								</div>
								<div className="flex items-center gap-1" title="Duplicate %">
									<span className="w-8 text-[10px] text-gray-500">Dup</span>
									<input type="text" inputMode="decimal" value={tcDraft.duplicatePct} onChange={(e) => setTcDraft((c) => ({ ...c, duplicatePct: e.target.value.replace(/[^0-9.]/g, "") }))} disabled={isBusy} className="h-6 w-full rounded border border-gray-200 px-1 text-[11px] outline-none focus:border-sky-400 disabled:opacity-60" />
								</div>
								<div className="flex items-center gap-1" title="Corrupt %">
									<span className="w-8 text-[10px] text-gray-500">Cor</span>
									<input type="text" inputMode="decimal" value={tcDraft.corruptPct} onChange={(e) => setTcDraft((c) => ({ ...c, corruptPct: e.target.value.replace(/[^0-9.]/g, "") }))} disabled={isBusy} className="h-6 w-full rounded border border-gray-200 px-1 text-[11px] outline-none focus:border-sky-400 disabled:opacity-60" />
								</div>
							</div>
							<div className="mt-2 flex items-center justify-between gap-2">
								<button type="button" onClick={() => onSetInterfaceTC(nodeId, iface.name, normalizedTC)} disabled={isBusy} className="h-6 rounded border border-gray-200 px-2 text-[11px] text-gray-700 hover:bg-gray-100 disabled:opacity-50 disabled:cursor-default">Apply</button>
								<button type="button" onClick={() => onClearInterfaceTC(nodeId, iface.name)} disabled={isBusy} className="h-6 rounded border border-gray-200 px-2 text-[11px] text-gray-700 hover:bg-gray-100 disabled:opacity-50 disabled:cursor-default">Clear</button>
							</div>
						</div>
					</div>
				</div>
			) : null}
		</div>
	);
}

function NodePanel({
	node,
	nodes,
	isBusy,
	recentCommands,
	lastCommand,
	onOpenTerminal,
	onToggleRun,
	onToggleFreeze,
	onSetInterfaceAddress,
	onUnsetInterfaceAddress,
	onSetInterfaceAdminState,
	onSetInterfaceFlap,
	onSetInterfaceTC,
	onClearInterfaceTC,
	onListRoutes,
	onAddRoute,
	onDeleteRoute,
	onExecuteNodeCommand,
	onRecordFailedNodeCommand,
	onClearRecentCommands,
	onRequestDeleteNode,
	onCollapse,
	onRenameNode,
	nodeSidebarState,
	onNodeSidebarStateChange,
}: {
	node: Node<SquareNodeData>;
	nodes: Node<SquareNodeData>[];
	isBusy: boolean;
	recentCommands: string[];
	lastCommand: SidebarLastCommand | null;
	onOpenTerminal: (nodeId: string) => void;
	onToggleRun: (nodeId: string) => void;
	onToggleFreeze: (nodeId: string) => void;
	onSetInterfaceAddress: (nodeId: string, interfaceName: string, cidr: string) => void;
	onUnsetInterfaceAddress: (nodeId: string, interfaceName: string) => void;
	onSetInterfaceAdminState: (nodeId: string, interfaceName: string, up: boolean) => void;
	onSetInterfaceFlap: (
		nodeId: string,
		interfaceName: string,
		enabled: boolean,
		options: { downMs: number; upMs: number; jitterMs: number },
	) => void;
	onSetInterfaceTC: (
		nodeId: string,
		interfaceName: string,
		options: {
			delayMs: number;
			jitterMs: number;
			lossPct: number;
			lossCorrelationPct: number;
			reorderPct: number;
			duplicatePct: number;
			corruptPct: number;
			bandwidthKbit: number;
			queueLimitPackets: number;
		},
	) => void;
	onClearInterfaceTC: (nodeId: string, interfaceName: string) => void;
	onListRoutes: (nodeId: string) => Promise<RouteRule[]>;
	onAddRoute: (nodeId: string, destination: string, gatewayOrBlackhole: string) => Promise<boolean>;
	onDeleteRoute: (nodeId: string, route: RouteRule) => Promise<void>;
	onExecuteNodeCommand: (nodeId: string, command: string, options?: { silent?: boolean }) => Promise<boolean>;
	onRecordFailedNodeCommand: (nodeId: string, command: string, errorMessage: string) => void;
	onClearRecentCommands: (nodeId: string) => void;
	onRequestDeleteNode: () => void;
	onCollapse: () => void;
	onRenameNode: (nodeId: string, displayName: string) => void;
	nodeSidebarState: NodeSidebarViewState;
	onNodeSidebarStateChange: (nodeId: string, next: NodeSidebarViewState) => void;
}) {
	const { data } = node;
	const nodeBusy = data.isBusy;
	const isRunning = data.status === "running" || data.status === "frozen";
	const canControlNodeNetworking = data.status === "running";
	const isFrozen = data.status === "frozen";
	const [interfacesCollapsed, setInterfacesCollapsed] = useState(nodeSidebarState.interfacesCollapsed);
	const [routesCollapsed, setRoutesCollapsed] = useState(nodeSidebarState.routesCollapsed);
	const [serversCollapsed, setServersCollapsed] = useState(nodeSidebarState.serversCollapsed);
	const [sendDataCollapsed, setSendDataCollapsed] = useState(nodeSidebarState.sendDataCollapsed);
	const [recentCollapsed, setRecentCollapsed] = useState(nodeSidebarState.recentCollapsed);
	const [actionsCollapsed, setActionsCollapsed] = useState(nodeSidebarState.actionsCollapsed);
	const [editingName, setEditingName] = useState(false);
	const [draftName, setDraftName] = useState(data.displayName);
	const [routeDestination, setRouteDestination] = useState("");
	const [routeNextHop, setRouteNextHop] = useState("");
	const [routeLeftMode, setRouteLeftMode] = useState<"text" | "default">("default");
	const [routeRightMode, setRouteRightMode] = useState<"text" | "blackhole">("text");
	const [routes, setRoutes] = useState<RouteRule[]>([]);
	const [routeBusy, setRouteBusy] = useState(false);
	const [destinationNodeId, setDestinationNodeId] = useState("");
	const [destinationInterfaceId, setDestinationInterfaceId] = useState("");
	const [destinationIP, setDestinationIP] = useState("");
	const [pingCount, setPingCount] = useState("4");
	const [tracerouteMaxHops, setTracerouteMaxHops] = useState("30");
	const [serverStatus, setServerStatus] = useState<Record<ServerName, ServerStatus>>({
		iperf: "stopped",
		http: "stopped",
		tcp: "stopped",
		udp: "stopped",
	});
	const [iperfMode, setIperfMode] = useState<"tcp" | "udp">("tcp");
	const [iperfTransferMode, setIperfTransferMode] = useState<"time" | "bytes">("time");
	const [iperfTransferValue, setIperfTransferValue] = useState("5");
	const [iperfBitrate, setIperfBitrate] = useState("");
	const [iperfPacketLength, setIperfPacketLength] = useState("");
	useEffect(() => setDraftName(data.displayName), [data.displayName]);
	useEffect(() => {
		setInterfacesCollapsed(nodeSidebarState.interfacesCollapsed);
		setRoutesCollapsed(nodeSidebarState.routesCollapsed);
		setServersCollapsed(nodeSidebarState.serversCollapsed);
		setSendDataCollapsed(nodeSidebarState.sendDataCollapsed);
		setRecentCollapsed(nodeSidebarState.recentCollapsed);
		setActionsCollapsed(nodeSidebarState.actionsCollapsed);
	}, [data.nodeId, nodeSidebarState]);

	useEffect(() => {
		onNodeSidebarStateChange(data.nodeId, {
			interfacesCollapsed,
			routesCollapsed,
			serversCollapsed,
			sendDataCollapsed,
			recentCollapsed,
			actionsCollapsed,
		});
	}, [
		actionsCollapsed,
		data.nodeId,
		interfacesCollapsed,
		onNodeSidebarStateChange,
		recentCollapsed,
		routesCollapsed,
		sendDataCollapsed,
		serversCollapsed,
	]);

	useEffect(() => {
		setServerStatus({ iperf: "stopped", http: "stopped", tcp: "stopped", udp: "stopped" });
	}, [data.nodeId]);

	const destinationNode = destinationNodeId ? nodes.find((n) => n.id === destinationNodeId) ?? null : null;
	const destinationInterfaces = destinationNode
		? destinationNode.data.interfaces.filter((iface) => iface.ipAddress !== "" && iface.prefixLength > 0)
		: [];

	useEffect(() => {
		if (!destinationNode) return;
		if (destinationInterfaces.length === 0) {
			setDestinationInterfaceId("");
			setDestinationIP("");
			return;
		}
		if (destinationInterfaces.find((iface) => iface.id === destinationInterfaceId)) return;
		const fallback = destinationInterfaces[0];
		setDestinationInterfaceId(fallback.id);
		setDestinationIP(fallback.ipAddress);
	}, [destinationInterfaceId, destinationInterfaces, destinationNode]);

	useEffect(() => {
		if (!destinationNode || destinationInterfaceId === "") return;
		const selected = destinationInterfaces.find((iface) => iface.id === destinationInterfaceId);
		if (!selected) return;
		setDestinationIP(selected.ipAddress);
	}, [destinationInterfaceId, destinationInterfaces, destinationNode]);

	useEffect(() => {
		if (!canControlNodeNetworking || routesCollapsed) return;
		let cancelled = false;
		void onListRoutes(data.nodeId).then((next) => {
			if (cancelled) return;
			setRoutes(next);
		}).catch(() => { });
		return () => { cancelled = true; };
	}, [canControlNodeNetworking, data.nodeId, onListRoutes, routesCollapsed]);

	const submitRoute = async () => {
		const destination = routeLeftMode === "default" ? "default" : routeDestination.trim();
		const gatewayOrBlackhole = routeRightMode === "blackhole" ? "blackhole" : routeNextHop.trim();
		if (!destination || !gatewayOrBlackhole) return;
		if (destination.toLowerCase() === "default" && gatewayOrBlackhole.toLowerCase() === "blackhole") return;
		setRouteBusy(true);
		try {
			const applied = await onAddRoute(data.nodeId, destination, gatewayOrBlackhole);
			if (!applied) return;
			const nextRoutes = await onListRoutes(data.nodeId);
			setRoutes(nextRoutes);
			if (routeLeftMode === "text") setRouteDestination("");
			if (routeRightMode === "text") setRouteNextHop("");
		} finally {
			setRouteBusy(false);
		}
	};

	const removeRoute = async (route: RouteRule) => {
		setRouteBusy(true);
		try {
			await onDeleteRoute(data.nodeId, route);
			const nextRoutes = await onListRoutes(data.nodeId);
			setRoutes(nextRoutes);
		} finally {
			setRouteBusy(false);
		}
	};

	const statusDot = nodeBusy ? "bg-blue-400" : isFrozen ? "bg-sky-300" : isRunning ? "bg-green-500" : "bg-gray-400";
	const statusLabel = nodeBusy ? "Loading" : isFrozen ? "Frozen" : isRunning ? "On" : "Off";
	const scopedLastCommand = lastCommand && lastCommand.nodeId === data.nodeId ? lastCommand : null;
	const canRunDataActions = data.type !== "switch" && canControlNodeNetworking;

	const submitComposedCommand = (command: string, errorMessage?: string) => {
		if (errorMessage) {
			onRecordFailedNodeCommand(data.nodeId, command, errorMessage);
			return;
		}
		void onExecuteNodeCommand(data.nodeId, command);
	};

	const submitServerToggle = (name: ServerName) => {
		const current = serverStatus[name];
		const nextCommand = current === "running" ? `${name} server stop` : `${name} server start`;
		setServerStatus((curr) => ({ ...curr, [name]: "loading" }));
		void onExecuteNodeCommand(data.nodeId, nextCommand, { silent: true }).then((ok) => {
			setServerStatus((curr) => ({ ...curr, [name]: ok ? (current === "running" ? "stopped" : "running") : current }));
		});
	};

	const runIperf = () => {
		const mode = iperfMode;
		const transferValue = iperfTransferValue.trim();
		const transferCount = parsePositiveInt(transferValue);
		const transferFlag = iperfTransferMode === "time" ? "--time" : "--bytes";
		const args: string[] = [];
		if (transferValue !== "") {
			args.push(`${transferFlag} ${transferValue}`);
		}
		if (mode === "udp" && iperfBitrate.trim() !== "") {
			args.push(`--bitrate ${iperfBitrate.trim()}`);
		}
		if (mode === "udp" && iperfPacketLength.trim() !== "") {
			args.push(`--packet-length ${iperfPacketLength.trim()}`);
		}

		const command = `iperf ${mode} ${destinationIP.trim()}${args.length > 0 ? ` ${args.join(" ")}` : ""}`;
		let err: string | undefined;
		if (!isValidIPv4(destinationIP)) err = "invalid target ip";
		if (!err && transferCount === null) {
			err = iperfTransferMode === "time" ? "time must be a positive integer" : "bytes must be a positive integer";
		}
		if (!err && mode === "udp" && iperfBitrate.trim() !== "" && !/^[1-9][0-9]*(\.[0-9]+)?[KMGkmg]?$/.test(iperfBitrate.trim())) {
			err = "bitrate must be a positive number with optional K, M, or G suffix";
		}
		if (!err && mode === "udp" && iperfPacketLength.trim() !== "") {
			const packetLength = parsePositiveInt(iperfPacketLength);
			if (packetLength === null || packetLength < 16 || packetLength > 65507) {
				err = "packet length must be between 16 and 65507 bytes";
			}
		}

		submitComposedCommand(command, err);
	};

	return (
		<div className="flex h-full flex-col overflow-hidden">
			{/* Header */}
			<div className="p-4 border-b border-gray-200 flex items-start justify-between flex-shrink-0">
				<div className="flex items-center gap-3">
					<NodeIcon type={data.type} />
					<div>
						<div className="relative h-8 w-[220px]">
							{editingName ? (
								<input
									value={draftName}
									onChange={(e) => setDraftName(e.target.value)}
									onBlur={() => {
										onRenameNode(data.nodeId, draftName);
										setEditingName(false);
									}}
									onKeyDown={(e) => {
										if (e.key === "Enter") {
											onRenameNode(data.nodeId, draftName);
											setEditingName(false);
										}
									}}
									className="absolute inset-0 h-8 w-[220px] rounded-md border border-blue-200 bg-blue-50 px-2.5 text-sm font-semibold text-gray-900 shadow-sm outline-none focus:border-blue-400 focus:bg-white"
									autoFocus
								/>
							) : (
								<button type="button" onClick={() => setEditingName(true)} className="absolute inset-0 flex h-8 w-[220px] items-center gap-1.5 text-left text-base font-semibold text-gray-900">
									<span className="min-w-0 flex-1 truncate">{data.displayName}</span>
									<PencilLine className="h-3.5 w-3.5 shrink-0 text-gray-400" />
								</button>
							)}
						</div>
						<p className="text-sm text-gray-500">{TYPE_LABEL[data.type] ?? data.type}</p>
					</div>
				</div>
				<button
					type="button"
					onClick={onCollapse}
					className="w-7 h-7 flex items-center justify-center rounded-md hover:bg-gray-100 transition-colors flex-shrink-0"
				>
					<X className="w-4 h-4 text-gray-500" />
				</button>
			</div>

			{/* Content */}
			<div className="flex min-h-0 flex-1 flex-col">
				<div className="min-h-0 flex-1 overflow-y-auto p-5 space-y-5">
					{/* Status */}
					<div className="flex items-center gap-2">
						<div className={`h-2 w-2 rounded-full ${statusDot}`} />
						<span className="text-sm text-gray-900">{statusLabel}</span>
					</div>

					{/* Actions */}
					<div>
						<button
							type="button"
							onClick={() => setActionsCollapsed((v) => !v)}
							className="mb-2 flex items-center gap-1 text-sm font-medium text-gray-700"
						>
							Actions
							<ChevronDown className={`h-4 w-4 transition-transform ${actionsCollapsed ? "-rotate-90" : ""}`} />
						</button>
						<div className={`overflow-hidden transition-all duration-200 ${actionsCollapsed ? "max-h-0 opacity-0 -translate-y-1" : "max-h-[320px] opacity-100 translate-y-0"}`}>
							<div className="space-y-2">
								<button
									type="button"
									onClick={() => onOpenTerminal(data.nodeId)}
									className="w-full h-9 px-3 flex items-center gap-2 rounded-lg border border-gray-200 bg-white hover:bg-gray-50 transition-colors text-sm"
								>
									<Terminal className="w-4 h-4" />
									Terminal
								</button>
								<button
									type="button"
									onClick={() => onToggleRun(data.nodeId)}
									disabled={nodeBusy}
									className="w-full h-9 px-3 flex items-center gap-2 rounded-lg border border-gray-200 bg-white hover:bg-gray-50 transition-colors text-sm disabled:opacity-50 disabled:cursor-default"
								>
									{isRunning ? (
										<><Square className="w-4 h-4" />Stop</>
									) : (
										<><Play className="w-4 h-4" />Start</>
									)}
								</button>
								{isRunning && (
									<button
										type="button"
										onClick={() => onToggleFreeze(data.nodeId)}
										disabled={nodeBusy}
										className="w-full h-9 px-3 flex items-center gap-2 rounded-lg border border-gray-200 bg-white hover:bg-gray-50 transition-colors text-sm disabled:opacity-50 disabled:cursor-default"
									>
										<Snowflake className="w-4 h-4 text-sky-400" />
										{isFrozen ? "Unfreeze" : "Freeze"}
									</button>
								)}
								<button
									type="button"
									onClick={onRequestDeleteNode}
									disabled={nodeBusy}
									className="w-full h-9 px-3 flex items-center gap-2 rounded-lg border border-red-200 bg-white hover:bg-red-50 hover:border-red-300 transition-colors text-sm text-red-600 disabled:opacity-50 disabled:cursor-default"
								>
									<Trash2 className="w-4 h-4" />
									Delete
								</button>
							</div>
						</div>
					</div>
					{/* Interfaces */}
					<div>
						<button
							type="button"
							onClick={() => setInterfacesCollapsed((v) => !v)}
							className="mb-2 flex items-center gap-1 text-sm font-medium text-gray-700"
						>
							Interfaces
							<ChevronDown className={`h-4 w-4 transition-transform ${interfacesCollapsed ? "-rotate-90" : ""}`} />
						</button>
						<div
							className={`overflow-hidden transition-all duration-200 ${interfacesCollapsed ? "max-h-0 -translate-y-2 opacity-0" : "max-h-[2000px] translate-y-0 opacity-100"}`}
						>
							<div className="space-y-2">
								{data.interfaces.map((iface) => {
									return (
										<InterfaceCard
											key={iface.id}
											nodeId={data.nodeId}
											iface={iface}
											isBusy={nodeBusy}
											canControl={canControlNodeNetworking}
											onSetInterfaceAddress={onSetInterfaceAddress}
											onUnsetInterfaceAddress={onUnsetInterfaceAddress}
											onSetInterfaceAdminState={onSetInterfaceAdminState}
											onSetInterfaceFlap={onSetInterfaceFlap}
											onSetInterfaceTC={onSetInterfaceTC}
											onClearInterfaceTC={onClearInterfaceTC}
										/>
									);
								})}
								{data.interfaces.length === 0 && (
									<div className="text-sm text-gray-400">No interfaces</div>
								)}
							</div>
						</div>
					</div>

					{/* Routing */}
					{data.type !== "switch" ? (
						<div>
							<button
								type="button"
								onClick={() => setRoutesCollapsed((v) => !v)}
								className="mb-2 flex items-center gap-1 text-sm font-medium text-gray-700"
							>
								Routing
								<ChevronDown className={`h-4 w-4 transition-transform ${routesCollapsed ? "-rotate-90" : ""}`} />
							</button>
							<div className={`overflow-hidden transition-all duration-200 ${routesCollapsed ? "max-h-0 -translate-y-2 opacity-0" : "max-h-[1200px] translate-y-0 opacity-100"}`}>
								{canControlNodeNetworking ? (
									<div className="space-y-2 rounded-lg border border-gray-200 bg-gray-50 p-2">
										<div className="flex items-center gap-2">
											<div className="flex min-w-0 flex-1 items-center gap-1">
												<button
													type="button"
													onClick={() => {
														setRouteLeftMode((m) => {
															const next = m === "default" ? "text" : "default";
															if (next === "default") setRouteRightMode("text");
															return next;
														});
													}}
													className={`flex h-7 w-7 items-center justify-center rounded border ${routeLeftMode === "default" ? "border-blue-300 bg-blue-100 text-blue-700" : "border-gray-200 bg-white text-gray-500"}`}
													title="Default destination"
												>
													<Route className="h-3.5 w-3.5" />
												</button>
												<input
													type="text"
													value={routeLeftMode === "default" ? "default" : routeDestination}
													onChange={(e) => setRouteDestination(e.target.value)}
													disabled={nodeBusy || routeBusy || routeLeftMode === "default"}
													placeholder="destination"
													className="h-7 min-w-0 flex-1 rounded border border-gray-200 bg-white px-2 text-xs outline-none focus:border-blue-300 disabled:opacity-60"
												/>
											</div>
											<ArrowRight className="h-3.5 w-3.5 text-gray-400" />
											<div className="flex min-w-0 flex-1 items-center gap-1">
												<input
													type="text"
													value={routeRightMode === "blackhole" ? "blackhole" : routeNextHop}
													onChange={(e) => setRouteNextHop(e.target.value)}
													disabled={nodeBusy || routeBusy || routeRightMode === "blackhole"}
													placeholder="next-hop"
													className="h-7 min-w-0 flex-1 rounded border border-gray-200 bg-white px-2 text-xs outline-none focus:border-blue-300 disabled:opacity-60"
												/>
												<button
													type="button"
													onClick={() => {
														setRouteRightMode((m) => {
															const next = m === "blackhole" ? "text" : "blackhole";
															if (next === "blackhole") setRouteLeftMode("text");
															return next;
														});
													}}
													className={`flex h-7 w-7 items-center justify-center rounded border ${routeRightMode === "blackhole" ? "border-slate-600 bg-slate-800 text-slate-100" : "border-gray-200 bg-white text-gray-500"}`}
													title="Blackhole next-hop"
												>
													<Ban className="h-3.5 w-3.5" />
												</button>
											</div>
										</div>
										<div className="flex justify-end">
											<button
												type="button"
												onClick={() => { void submitRoute(); }}
												disabled={
													nodeBusy
													|| routeBusy
													|| (routeLeftMode === "text" && routeDestination.trim() === "")
													|| (routeRightMode === "text" && routeNextHop.trim() === "")
												}
												className="h-7 rounded border border-gray-200 bg-white px-2 text-xs text-gray-700 hover:bg-gray-100 disabled:opacity-50 disabled:cursor-default"
											>
												Apply
											</button>
										</div>
										<div className="space-y-1">
											{routes.length > 0 ? routes.map((route, idx) => (
												<div key={`${route.kind}-${route.destination}-${route.nextHop}-${idx}`} className="flex items-center justify-between gap-2 rounded border border-gray-200 bg-white px-2 py-1.5">
													<div className="min-w-0 text-xs text-gray-700">
														{route.kind === "blackhole"
															? `${route.destination} -> blackhole`
															: `${route.destination} -> ${route.nextHop}`}
													</div>
													<button
														type="button"
														onClick={() => { void removeRoute(route); }}
														disabled={nodeBusy || routeBusy}
														className="rounded p-1 text-gray-400 hover:bg-red-50 hover:text-red-600 disabled:opacity-50 disabled:cursor-default"
														aria-label="Delete route"
													>
														<Trash2 className="h-3.5 w-3.5" />
													</button>
												</div>
											)) : (
												<div className="text-xs text-gray-400">No routes</div>
											)}
										</div>
									</div>
								) : (
									<div className="text-xs text-gray-400">{isFrozen ? "Unfreeze node to edit routing" : "Start node to edit routing"}</div>
								)}
							</div>
						</div>
					) : null}

					{/* Servers */}
					{data.type !== "switch" ? (
						<div>
							<button
								type="button"
								onClick={() => setServersCollapsed((v) => !v)}
								className="mb-2 flex items-center gap-1 text-sm font-medium text-gray-700"
							>
								Server
								<ChevronDown className={`h-4 w-4 transition-transform ${serversCollapsed ? "-rotate-90" : ""}`} />
							</button>
							<div className={`overflow-hidden transition-all duration-200 ${serversCollapsed ? "max-h-0 -translate-y-2 opacity-0" : "max-h-[1400px] translate-y-0 opacity-100"}`}>
								{canControlNodeNetworking ? (
									<div className="grid grid-cols-2 gap-2">
										{([
											{ name: "iperf", logsSupported: true },
											{ name: "http", logsSupported: true },
											{ name: "tcp", logsSupported: false },
											{ name: "udp", logsSupported: false },
										] as { name: ServerName; logsSupported: boolean }[]).map((service) => {
											const state = serverStatus[service.name];
											const isLoading = state === "loading";
											const isRunning = state === "running";
											const statusClasses = isLoading
												? "border-yellow-300 bg-yellow-50"
												: isRunning
													? "border-green-300 bg-green-50"
													: "border-red-300 bg-red-50";
											return (
												<div key={service.name} className={`rounded-lg border p-2 ${statusClasses}`}>
													<button
														type="button"
														onClick={() => submitServerToggle(service.name)}
														disabled={nodeBusy || isLoading}
														className={`flex h-8 w-full items-center justify-between rounded border border-gray-200 bg-white px-2 text-xs text-gray-700 transition-colors disabled:opacity-60 ${service.logsSupported ? "mb-2" : ""}`}
													>
														<span className="flex items-center gap-1.5 uppercase">
															<Server className="h-3.5 w-3.5" />
															{service.name}
														</span>
														{isLoading ? <Loader2 className="h-3.5 w-3.5 animate-spin text-yellow-500" /> : null}
													</button>
													{service.logsSupported ? (
														<div className="grid grid-cols-2 gap-1">
															<button
																type="button"
																onClick={() => submitComposedCommand(`${service.name} server log`)}
																disabled={nodeBusy || isLoading}
																title="View log"
																className="flex h-7 items-center justify-center gap-1 rounded border border-gray-200 bg-white px-2 text-[11px] text-gray-700 hover:bg-gray-100 disabled:opacity-50"
															>
																<ScrollText className="h-3 w-3" />
																Log
															</button>
															<button
																type="button"
																onClick={() => void onExecuteNodeCommand(data.nodeId, `${service.name} server log clear`, { silent: true })}
																disabled={nodeBusy || isLoading}
																title="Clear log"
																className="flex h-7 items-center justify-center gap-1 rounded border border-gray-200 bg-white px-2 text-[11px] text-gray-700 hover:bg-gray-100 disabled:opacity-50"
															>
																<ScrollText className="h-3 w-3" />
																Clear
															</button>
														</div>
													) : null}
												</div>
											);
										})}
									</div>
								) : (
									<div className="text-xs text-gray-400">{isFrozen ? "Unfreeze node to control servers" : "Start node to control servers"}</div>
								)}
							</div>
						</div>
					) : null}

					{/* Send Data */}
					{data.type !== "switch" ? (
						<div>
							<button
								type="button"
								onClick={() => setSendDataCollapsed((v) => !v)}
								className="mb-2 flex items-center gap-1 text-sm font-medium text-gray-700"
							>
								Send traffic
								<ChevronDown className={`h-4 w-4 transition-transform ${sendDataCollapsed ? "-rotate-90" : ""}`} />
							</button>
							<div className={`overflow-hidden transition-all duration-200 ${sendDataCollapsed ? "max-h-0 -translate-y-2 opacity-0" : "max-h-[2200px] translate-y-0 opacity-100"}`}>
								{canRunDataActions ? (
									<div className="space-y-2">
										<div className="rounded-lg border border-gray-200 bg-gray-50 p-2">
											<div className="mb-2 text-[11px] font-medium uppercase text-gray-500">Destination</div>
											<div className="grid grid-cols-2 gap-2">
												<select
													value={destinationNodeId}
													onChange={(e) => {
														setDestinationNodeId(e.target.value);
													}}
													className="h-8 rounded border border-gray-200 bg-white px-2 text-xs outline-none focus:border-blue-300"
												>
													<option value="">Destination node</option>
													{nodes.filter((n) => n.id !== data.nodeId).map((n) => (
														<option key={n.id} value={n.id}>{n.data.displayName}</option>
													))}
												</select>
												{destinationInterfaces.length > 1 ? (
													<select value={destinationInterfaceId} onChange={(e) => setDestinationInterfaceId(e.target.value)} className="h-8 rounded border border-gray-200 bg-white px-2 text-xs outline-none focus:border-blue-300">
														{destinationInterfaces.map((iface) => (
															<option key={iface.id} value={iface.id}>{iface.name} ({iface.ipAddress})</option>
														))}
													</select>
												) : destinationInterfaces.length === 1 ? (
													<div className="h-8 rounded border border-gray-200 bg-white px-2 text-xs leading-8 text-gray-600">
														{destinationInterfaces[0].name} ({destinationInterfaces[0].ipAddress})
													</div>
												) : (
													<div className="h-8 rounded border border-gray-200 bg-white px-2 text-[11px] leading-8 text-gray-500">
														{destinationNode && destinationInterfaces.length === 0 ? "No assigned IPs" : "Select a destination"}
													</div>
												)}
											</div>
											<div className="mt-2 flex items-center gap-2">
												<input value={destinationIP} onChange={(e) => setDestinationIP(e.target.value)} placeholder="Destination IPv4" className="h-8 min-w-0 flex-1 rounded border border-gray-200 bg-white px-2 text-xs outline-none focus:border-blue-300" />
											</div>
										</div>

										<div className="rounded-lg border border-gray-200 bg-gray-50 p-2">
											<div className="mb-2 text-[11px] font-medium uppercase text-gray-500">Connectivity Checks</div>
											<div className="grid grid-cols-2 gap-2">
												<div className="rounded border border-gray-200 bg-white p-2">
													<div className="mb-2">
														<span className="text-xs font-medium text-gray-700">Ping</span>
													</div>
													<div className="mb-2 flex justify-end">
														<div className="flex items-center gap-1">
															<span className="whitespace-nowrap text-[10px] text-gray-500">packet count</span>
															<input value={pingCount} onChange={(e) => setPingCount(e.target.value.replace(/\D/g, ""))} className="h-6 w-6 rounded border border-gray-200 px-1 text-[11px]" />
														</div>
													</div>
													<button type="button" onClick={() => {
														const command = `ping ${destinationIP.trim()} --count ${pingCount.trim() || "0"}`;
														const n = parsePositiveInt(pingCount);
														const err = !isValidIPv4(destinationIP) ? "invalid target ip" : n === null ? "packet count must be a positive integer" : undefined;
														submitComposedCommand(command, err);
													}} disabled={nodeBusy} className="h-7 w-full rounded border border-gray-200 bg-white px-2 text-[11px] text-gray-700 hover:bg-gray-100 disabled:opacity-50">Send ping</button>
												</div>
												<div className="rounded border border-gray-200 bg-white p-2">
													<div className="mb-2">
														<span className="text-xs font-medium text-gray-700">Traceroute</span>
													</div>
													<div className="mb-2 flex justify-end">
														<div className="flex items-center gap-1">
															<span className="whitespace-nowrap text-[10px] text-gray-500">max hops</span>
															<input value={tracerouteMaxHops} onChange={(e) => setTracerouteMaxHops(e.target.value.replace(/\D/g, ""))} className="h-6 w-6 rounded border border-gray-200 px-1 text-[11px]" />
														</div>
													</div>
													<button type="button" onClick={() => {
														const command = `traceroute ${destinationIP.trim()} --max-hops ${tracerouteMaxHops.trim() || "0"}`;
														const n = parsePositiveInt(tracerouteMaxHops);
														const err = !isValidIPv4(destinationIP) ? "invalid target ip" : n === null ? "max hops must be an integer" : n < 1 || n > 255 ? "max hops must be between 1 and 255" : undefined;
														submitComposedCommand(command, err);
													}} disabled={nodeBusy} className="h-7 w-full rounded border border-gray-200 bg-white px-2 text-[11px] text-gray-700 hover:bg-gray-100 disabled:opacity-50">Trace path</button>
												</div>
												<button type="button" onClick={() => {
													const command = `tcp connect ${destinationIP.trim()}`;
													submitComposedCommand(command, !isValidIPv4(destinationIP) ? "invalid target ip" : undefined);
												}} disabled={nodeBusy} className="h-8 rounded border border-gray-200 bg-white px-2 text-xs text-gray-700 hover:bg-gray-100 disabled:opacity-50">TCP connect</button>
												<button type="button" onClick={() => {
													const command = `udp probe ${destinationIP.trim()}`;
													submitComposedCommand(command, !isValidIPv4(destinationIP) ? "invalid target ip" : undefined);
												}} disabled={nodeBusy} className="h-8 rounded border border-gray-200 bg-white px-2 text-xs text-gray-700 hover:bg-gray-100 disabled:opacity-50">UDP probe</button>
												<button type="button" onClick={() => {
													const command = `http get ${destinationIP.trim()}`;
													submitComposedCommand(command, !isValidIPv4(destinationIP) ? "invalid target ip" : undefined);
												}} disabled={nodeBusy} className="col-span-2 h-8 rounded border border-gray-200 bg-white px-2 text-xs text-gray-700 hover:bg-gray-100 disabled:opacity-50">HTTP get</button>
											</div>
										</div>

										<div className="rounded-lg border border-gray-200 bg-gray-50 p-2">
											<div className="mb-2 text-[11px] font-medium uppercase text-gray-500">iperf</div>
											<div className="rounded border border-gray-200 bg-white p-2">
												<div className="mb-2 grid grid-cols-2 gap-2">
													<div className="flex rounded border border-gray-200 bg-gray-50 p-0.5">
														<button type="button" onClick={() => setIperfMode("tcp")} className={`h-7 flex-1 rounded text-xs ${iperfMode === "tcp" ? "bg-white text-gray-900 shadow-sm" : "text-gray-500"}`}>TCP</button>
														<button type="button" onClick={() => setIperfMode("udp")} className={`h-7 flex-1 rounded text-xs ${iperfMode === "udp" ? "bg-white text-gray-900 shadow-sm" : "text-gray-500"}`}>UDP</button>
													</div>
													<div className="flex rounded border border-gray-200 bg-gray-50 p-0.5">
														<button
															type="button"
															onClick={() => {
																setIperfTransferMode("time");
																if (iperfTransferValue === "") setIperfTransferValue("5");
															}}
															className={`h-7 flex-1 rounded text-xs ${iperfTransferMode === "time" ? "bg-white text-gray-900 shadow-sm" : "text-gray-500"}`}
														>
															Time
														</button>
														<button
															type="button"
															onClick={() => {
																setIperfTransferMode("bytes");
																if (iperfTransferValue === "") setIperfTransferValue("1024");
															}}
															className={`h-7 flex-1 rounded text-xs ${iperfTransferMode === "bytes" ? "bg-white text-gray-900 shadow-sm" : "text-gray-500"}`}
														>
															Bytes
														</button>
													</div>
												</div>
												<div className="grid grid-cols-2 gap-2">
													<div>
														<div className="mb-1 text-[11px] text-gray-500">{iperfTransferMode === "time" ? "Seconds" : "Bytes"}</div>
														<input value={iperfTransferValue} onChange={(e) => setIperfTransferValue(e.target.value.replace(/\D/g, ""))} className="h-8 w-full rounded border border-gray-200 px-2 text-xs outline-none focus:border-blue-300" />
													</div>
													{iperfMode === "udp" ? (
														<div>
															<div className="mb-1 text-[11px] text-gray-500">Bitrate (optional)</div>
															<input value={iperfBitrate} onChange={(e) => setIperfBitrate(e.target.value)} placeholder="10M" className="h-8 w-full rounded border border-gray-200 px-2 text-xs outline-none focus:border-blue-300" />
														</div>
													) : (
														<div />
													)}
													{iperfMode === "udp" ? (
														<div className="col-span-2">
															<div className="mb-1 text-[11px] text-gray-500">Packet length (16..65507, optional)</div>
															<input value={iperfPacketLength} onChange={(e) => setIperfPacketLength(e.target.value.replace(/\D/g, ""))} className="h-8 w-full rounded border border-gray-200 px-2 text-xs outline-none focus:border-blue-300" />
														</div>
													) : null}
												</div>
												<button type="button" onClick={runIperf} disabled={nodeBusy} className="mt-2 h-8 w-full rounded border border-gray-200 bg-white px-2 text-xs text-gray-700 hover:bg-gray-100 disabled:opacity-50">Run iperf</button>
											</div>
										</div>
									</div>
								) : (
									<div className="text-xs text-gray-400">{isFrozen ? "Unfreeze node to send traffic" : "Start node to send traffic"}</div>
								)}
							</div>
						</div>
					) : null}

					{/* Recent commands */}
					<div>
							<div className="mb-2 flex items-center justify-between">
								<button
									type="button"
									onClick={() => setRecentCollapsed((v) => !v)}
									className="flex items-center gap-1 text-sm font-medium text-gray-700"
								>
									Recent commands
									<ChevronDown className={`h-4 w-4 transition-transform ${recentCollapsed ? "-rotate-90" : ""}`} />
								</button>
								{recentCommands.length > 0 ? (
									<button
										type="button"
										onClick={() => onClearRecentCommands(data.nodeId)}
										className={`inline-flex h-6 items-center gap-1 rounded border border-gray-200 bg-white px-2 text-[11px] text-gray-700 hover:bg-gray-100 ${recentCollapsed ? "invisible" : ""}`}
										title="Clear recent commands"
									>
										<RotateCcw className="h-3 w-3" />
									</button>
								) : null}
							</div>
							<div className={`overflow-hidden transition-all duration-200 ${recentCollapsed ? "max-h-0 -translate-y-2 opacity-0" : "max-h-[1200px] translate-y-0 opacity-100"}`}>
								<div className="space-y-1">
									{recentCommands.length > 0 ? recentCommands.slice().reverse().map((cmd, idx) => (
										<div key={idx} className="rounded bg-gray-50 px-2 py-1 font-mono text-xs text-gray-600">
											{cmd}
										</div>
									)) : (
										<div className="text-xs text-gray-400">No commands yet</div>
									)}
								</div>
							</div>
						</div>
					
				</div>

				{/* Last command */}
				<div className="flex-shrink-0 border-t border-gray-200 pt-3 pb-0">
					<div className="mb-2 px-5 text-sm font-medium text-gray-700">Last command</div>
					{scopedLastCommand ? (
						<div className="h-20 overflow-y-auto pl-5 pr-0">
							<div className="grid grid-cols-[16px_minmax(0,1fr)] gap-x-2 gap-y-1 pr-3">
								<div className="row-start-1 col-start-1 mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center">
									{scopedLastCommand.status === "executing" ? (
										<Loader2 className="h-4 w-4 animate-spin text-yellow-500" />
									) : scopedLastCommand.status === "success" || scopedLastCommand.status === "done" ? (
										<CheckCircle className="h-4 w-4 text-green-500" />
									) : scopedLastCommand.status === "failed" ? (
										<AlertCircle className="h-4 w-4 text-red-500" />
									) : (
										<div className="mt-1 h-2 w-2 rounded-full bg-gray-400" />
									)}
								</div>
								<div className="row-start-1 col-start-2 text-sm capitalize text-gray-900">{scopedLastCommand.status}</div>
								<div className="row-start-2 col-span-2 whitespace-pre-wrap break-words text-sm text-gray-500">{scopedLastCommand.command}</div>
								<div className={`row-start-3 col-span-2 whitespace-pre-wrap break-words text-xs ${scopedLastCommand.status === "failed" && scopedLastCommand.errorMessage ? "text-red-600" : "text-transparent"}`}>
									{scopedLastCommand.status === "failed" && scopedLastCommand.errorMessage ? scopedLastCommand.errorMessage : "."}
								</div>
							</div>
						</div>
					) : (
						<div className="h-20 px-5 text-xs text-gray-400">No command executed yet</div>
					)}
				</div>
			</div>
		</div>
	);
}

function EdgePanel({
	edge,
	nodes,
	isBusy,
	onDeleteLink,
	onCollapse,
}: {
	edge: Edge<InterfaceLabelEdgeData>;
	nodes: Node<SquareNodeData>[];
	isBusy: boolean;
	onDeleteLink: () => void;
	onCollapse: () => void;
}) {
	const sourceNode = nodes.find((n) => n.id === edge.source);
	const targetNode = nodes.find((n) => n.id === edge.target);
	const { data } = edge;

	return (
		<div className="flex h-full flex-col overflow-hidden">
			{/* Header */}
			<div className="p-4 border-b border-gray-200 flex items-start justify-between flex-shrink-0">
				<div>
					<h2 className="text-base font-semibold text-gray-900">Link</h2>
				</div>
				<button
					type="button"
					onClick={onCollapse}
					className="w-7 h-7 flex items-center justify-center rounded-md hover:bg-gray-100 transition-colors flex-shrink-0"
				>
					<X className="w-4 h-4 text-gray-500" />
				</button>
			</div>

			{/* Content */}
			<div className="flex-1 overflow-y-auto p-5 space-y-5">
				<div className="p-3 bg-gray-50 rounded-lg border border-gray-200">
					<div className="flex items-center gap-2 mb-1">
						{sourceNode && <NodeIcon type={sourceNode.data.type} className="w-4 h-4 text-gray-500" />}
						<div className="max-w-[180px] truncate text-sm font-medium text-gray-900">{sourceNode?.data.displayName ?? edge.source}</div>
					</div>
					<div className="text-xs text-gray-500">{data?.interfaceAName}</div>
					{data?.interfaceAIP && <div className="text-xs text-gray-500 mt-0.5">{data.interfaceAIP}</div>}
				</div>

				<div className="p-3 bg-gray-50 rounded-lg border border-gray-200">
					<div className="flex items-center gap-2 mb-1">
						{targetNode && <NodeIcon type={targetNode.data.type} className="w-4 h-4 text-gray-500" />}
						<div className="max-w-[180px] truncate text-sm font-medium text-gray-900">{targetNode?.data.displayName ?? edge.target}</div>
					</div>
					<div className="text-xs text-gray-500">{data?.interfaceBName}</div>
					{data?.interfaceBIP && <div className="text-xs text-gray-500 mt-0.5">{data.interfaceBIP}</div>}
				</div>

				<div>
					<h3 className="text-sm font-medium text-gray-700 mb-3">Actions</h3>
					<button
						type="button"
						onClick={onDeleteLink}
						disabled={isBusy}
						className="w-full h-9 px-3 flex items-center gap-2 rounded-lg border border-red-200 bg-white hover:bg-red-50 hover:border-red-300 transition-colors text-sm text-red-600 disabled:opacity-50 disabled:cursor-default"
					>
						<Trash2 className="w-4 h-4" />
						Unlink
					</button>
				</div>
			</div>
		</div>
	);
}

export function Sidebar({
	selectedNode,
	selectedEdge,
	nodes,
	edges: _edges,
	isBusy,
	isCollapsed,
	recentCommands,
	lastCommand,
	onRenameNode,
	onOpenTerminal,
	onToggleRun,
	onToggleFreeze,
	onSetInterfaceAddress,
	onUnsetInterfaceAddress,
	onSetInterfaceAdminState,
	onSetInterfaceFlap,
	onSetInterfaceTC,
	onClearInterfaceTC,
	onListRoutes,
	onAddRoute,
	onDeleteRoute,
	onExecuteNodeCommand,
	onRecordFailedNodeCommand,
	onClearRecentCommands,
	onRequestDeleteNode,
	onDeleteLink,
	onToggleCollapse,
	nodeSidebarStateByNodeId,
	onNodeSidebarStateChange,
}: SidebarProps) {
	const hasSelection = selectedNode !== null || selectedEdge !== null;
	const hidden = !hasSelection && isCollapsed;

	if (hidden) return null;

	return (
		<aside
			className={`fixed right-0 top-14 z-[700] flex flex-col border-l border-gray-200 bg-white overflow-hidden transition-all duration-200 [&_button:not(:disabled)]:cursor-pointer ${isCollapsed ? "w-0 opacity-0" : "w-[320px] opacity-100"
				}`}
			style={{
				height: "calc(100vh - 56px)",
			}}
		>
			{selectedNode ? (
				<NodePanel
					key={selectedNode.id}
					node={selectedNode}
					nodes={nodes}
					isBusy={isBusy}
					recentCommands={recentCommands}
					lastCommand={lastCommand}
					onOpenTerminal={onOpenTerminal}
					onToggleRun={onToggleRun}
					onToggleFreeze={onToggleFreeze}
					onSetInterfaceAddress={onSetInterfaceAddress}
					onUnsetInterfaceAddress={onUnsetInterfaceAddress}
					onSetInterfaceAdminState={onSetInterfaceAdminState}
					onSetInterfaceFlap={onSetInterfaceFlap}
					onSetInterfaceTC={onSetInterfaceTC}
					onClearInterfaceTC={onClearInterfaceTC}
					onListRoutes={onListRoutes}
					onAddRoute={onAddRoute}
					onDeleteRoute={onDeleteRoute}
					onExecuteNodeCommand={onExecuteNodeCommand}
					onRecordFailedNodeCommand={onRecordFailedNodeCommand}
					onClearRecentCommands={onClearRecentCommands}
					onRequestDeleteNode={onRequestDeleteNode}
					onCollapse={onToggleCollapse}
					onRenameNode={onRenameNode}
					nodeSidebarState={nodeSidebarStateByNodeId[selectedNode.id] ?? {
						interfacesCollapsed: false,
						routesCollapsed: true,
						serversCollapsed: true,
						sendDataCollapsed: true,
						recentCollapsed: true,
						actionsCollapsed: true,
					}}
					onNodeSidebarStateChange={onNodeSidebarStateChange}
				/>
			) : selectedEdge ? (
				<EdgePanel
					edge={selectedEdge}
					nodes={nodes}
					isBusy={isBusy}
					onDeleteLink={onDeleteLink}
					onCollapse={onToggleCollapse}
				/>
			) : null}
		</aside>
	);
}

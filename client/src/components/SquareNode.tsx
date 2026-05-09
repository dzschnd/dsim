import { Monitor, Network, Router as RouterIcon, Terminal, Play, Square } from "lucide-react";
import type { NodeProps } from "reactflow";
import type { ApiInterface } from "../services/topology";
import { SideHandles } from "./SideHandles";

export type SquareNodeData = {
	nodeId: string;
	displayName: string;
	type: string;
	status: string;
	interfaces: ApiInterface[];
	isSelected: boolean;
	isBusy: boolean;
	connectionSourceNodeId: string;
	onToggleRun: () => void;
	onOpenTerminal: () => void;
};

function NodeTypeIcon({ type, className }: { type: string; className?: string }) {
	const cls = className ?? "h-6 w-6 shrink-0 text-gray-600";
	if (type === "router") return <RouterIcon className={cls} />;
	if (type === "switch") return <Network className={cls} />;
	return <Monitor className={cls} />;
}

const TYPE_LABEL: Record<string, string> = {
	host: "Host",
	switch: "Switch",
	router: "Router",
};

export function SquareNode({ data }: NodeProps<SquareNodeData>) {
	const isRunning = data.status === "running" || data.status === "frozen";
	const isPowerBusy = data.isBusy;
	const nodeWidth = 160;

	const statusDot = data.status === "frozen"
		? "bg-sky-300"
		: isRunning
			? "bg-green-500"
			: "bg-gray-400";

	const statusLabel = data.status === "frozen" ? "Frozen" : isRunning ? "On" : "Off";

	return (
		<div
			className={`relative bg-white rounded-xl p-3 border-2 cursor-pointer transition-all select-none ${data.isSelected
				? "shadow-lg"
				: "border-gray-200 hover:border-gray-300 hover:shadow-md"
				}`}
			style={{ width: `${nodeWidth}px`, borderColor: data.isSelected ? "#6b8fd6" : undefined }}
		>
			{/* Top action buttons */}
			<div className="flex items-start justify-between mb-2">
				<button
					type="button"
					onClick={(e) => {
						e.stopPropagation();
						data.onOpenTerminal();
					}}
					className="w-6 h-6 flex items-center justify-center rounded transition-colors nodrag nopan hover:bg-gray-100"
					aria-label="Open terminal"
				>
					<Terminal className="w-3.5 h-3.5 text-gray-600" />
				</button>
				<button
					type="button"
					onClick={(e) => {
						e.stopPropagation();
						if (!isPowerBusy) data.onToggleRun();
					}}
					disabled={isPowerBusy}
					className={`w-6 h-6 flex items-center justify-center rounded transition-colors nodrag nopan ${isPowerBusy ? "opacity-50 cursor-default" : "hover:bg-gray-100"
						}`}
					aria-label={isRunning ? "Stop" : "Start"}
				>
					{isRunning ? (
						<Square className="w-3.5 h-3.5 text-gray-600" />
					) : (
						<Play className="w-3.5 h-3.5 text-gray-600" />
					)}
				</button>
			</div>

			{/* Icon + name/type */}
			<div className="mb-2 -mt-2 flex items-center gap-2">
				<NodeTypeIcon type={data.type} className="translate-y-1 h-6 w-6 shrink-0 self-center text-gray-600" />
				<div className="min-w-0">
					<div className=" truncate text-sm pb-2 translate-y-1 font-medium text-gray-900">{data.displayName}</div>
					<div className={`text-xs text-gray-500 pt-2`}> {TYPE_LABEL[data.type] ?? data.type}</div>
				</div>
			</div>

			{/* Status */}
			<div className="mb-3 flex items-center gap-1.5">
				<div className={`h-1.5 w-1.5 rounded-full ${statusDot}`} />
				<span className="text-xs text-gray-600">{statusLabel}</span>
			</div>

			<SideHandles
				currentNodeId={data.nodeId}
				connectionSourceNodeId={data.connectionSourceNodeId}
				nodeType={data.type}
			/>
		</div >
	);
}

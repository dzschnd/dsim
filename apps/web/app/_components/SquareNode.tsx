import type { ApiInterface } from "../services/topology";
import type { NodeProps } from "reactflow";

import { NodeTerminal } from "./NodeTerminal";
import { SideHandles } from "./SideHandles";

export type SquareNodeData = {
	label: string;
	nodeId: string;
	type: string;
	status: string;
	containerId: string;
	interfaces: ApiInterface[];
	isSelected: boolean;
	isBusy: boolean;
	connectionSourceNodeId: string;
	isTerminalOpen: boolean;
	isTerminalFullscreen: boolean;
	terminalInput: string;
	terminalLines: string[];
	onToggleRun: () => void;
	onToggleTerminal: () => void;
	onToggleTerminalFullscreen: () => void;
	onTerminalInputChange: (value: string) => void;
	onTerminalSubmit: () => void;
};

export function SquareNode({ data }: NodeProps<SquareNodeData>) {
	const isRunning = data.status === "running";
	const nodeClass = data.isSelected
		? `relative z-10 flex h-[160px] w-[160px] cursor-pointer select-none items-center justify-center border-2 p-3 text-center shadow-sm ring-4 ring-blue-500/20 ${data.isTerminalOpen ? "z-[900]" : ""} ${isRunning
			? "border-blue-600 bg-emerald-50 shadow-emerald-700/10"
			: "border-blue-600 bg-zinc-100 shadow-slate-500/10"
		}`
		: `relative z-10 flex h-[160px] w-[160px] cursor-pointer select-none items-center justify-center border-2 p-3 text-center shadow-sm ${data.isTerminalOpen ? "z-[900]" : ""} ${isRunning
			? "border-emerald-700 bg-emerald-50 shadow-emerald-700/10"
			: "border-slate-500 bg-zinc-100 shadow-slate-500/10"
		}`;

	return (
		<div className={nodeClass}>
			{isRunning ? (
				<button
					type="button"
					onClick={(event) => {
						event.stopPropagation();
						data.onToggleTerminal();
					}}
					className="nodrag nopan absolute left-2 top-2 flex h-7 w-7 items-center justify-center rounded border border-slate-400 bg-white/90 font-mono text-[11px] font-semibold text-slate-800 hover:bg-slate-100"
					aria-label={data.isTerminalOpen ? "Hide terminal" : "Show terminal"}
				>
					&gt;_
				</button>
			) : null}
			<button
				type="button"
				onClick={(event) => {
					event.stopPropagation();
					void data.onToggleRun();
				}}
				disabled={data.isBusy}
				className="nodrag nopan absolute right-2 top-2 flex h-7 w-7 items-center justify-center rounded border border-slate-400 bg-white/90 hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50"
				aria-label={isRunning ? "Pause node" : "Run node"}
			>
				{isRunning ? (
					<div className="flex gap-[3px]">
						<span className="block h-3.5 w-1 bg-slate-800" />
						<span className="block h-3.5 w-1 bg-slate-800" />
					</div>
				) : (
					<div className="h-0 w-0 border-y-[7px] border-l-[11px] border-r-0 border-y-transparent border-l-slate-800" />
				)}
			</button>
			<div className="pointer-events-none flex flex-col items-center gap-4 -translate-y-0.75">
				<div className="text-[13px] font-semibold leading-tight text-zinc-900">{data.label}</div>
				<div className="text-[11px] leading-tight text-zinc-500">{data.type}</div>
			</div>
			{isRunning && data.isTerminalOpen ? (
				<NodeTerminal
					terminalLines={data.terminalLines}
					terminalInput={data.terminalInput}
					isFullscreen={data.isTerminalFullscreen}
					onInputChange={data.onTerminalInputChange}
					onSubmit={data.onTerminalSubmit}
					onToggleFullscreen={data.onToggleTerminalFullscreen}
				/>
			) : null}
			<SideHandles currentNodeId={data.nodeId} connectionSourceNodeId={data.connectionSourceNodeId} />
		</div>
	);
}

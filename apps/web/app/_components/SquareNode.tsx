import type { NodeProps } from "reactflow";

import { SideHandles } from "./SideHandles";

export type SquareNodeData = {
	label: string;
	type: string;
	status: string;
	containerId: string;
	isSelected: boolean;
	isBusy: boolean;
	isTerminalOpen: boolean;
	terminalInput: string;
	terminalLines: string[];
	onToggleRun: () => void;
	onToggleTerminal: () => void;
	onTerminalInputChange: (value: string) => void;
	onTerminalSubmit: () => void;
};

export function SquareNode({ data }: NodeProps<SquareNodeData>) {
	const isRunning = data.status === "running";
	const nodeClass = data.isSelected
		? `relative flex h-[160px] w-[160px] cursor-pointer select-none items-center justify-center border-2 p-3 text-center shadow-sm ring-4 ring-blue-500/20 ${isRunning
			? "border-blue-600 bg-emerald-50 shadow-emerald-700/10"
			: "border-blue-600 bg-zinc-100 shadow-slate-500/10"
		}`
		: `relative flex h-[160px] w-[160px] cursor-pointer select-none items-center justify-center border-2 p-3 text-center shadow-sm ${isRunning
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
			<div className="pointer-events-none flex flex-col gap-2">
				<div className="text-[13px] font-semibold leading-tight text-zinc-900">{data.label}</div>
				<div className="text-[11px] leading-tight text-zinc-500">{data.type}</div>
			</div>
			{isRunning && data.isTerminalOpen ? (
				<div className="nodrag nopan absolute bottom-full left-1/2 z-20 mb-2 flex h-44 w-64 -translate-x-1/2 flex-col overflow-hidden rounded border border-slate-800 bg-zinc-950 text-left font-mono text-[11px] text-zinc-100 shadow-lg">
					<div className="flex-1 overflow-y-scroll px-3 py-2">
						{data.terminalLines.length > 0 && (
							data.terminalLines.map((line, index) => (
								<div key={`${line}-${index}`} className="leading-5 text-zinc-300">
									{line}
								</div>
							))
						)}
					</div>
					<div className="flex items-center gap-2 border-t border-zinc-800 px-3 py-2">
						<span className="text-emerald-400">$</span>
						<input
							type="text"
							value={data.terminalInput}
							onChange={(event) => {
								event.stopPropagation();
								data.onTerminalInputChange(event.target.value);
							}}
							onClick={(event) => {
								event.stopPropagation();
							}}
							onPointerDown={(event) => {
								event.stopPropagation();
							}}
							onKeyDown={(event) => {
								event.stopPropagation();
								if (event.key === "Enter") {
									event.preventDefault();
									data.onTerminalSubmit();
								}
							}}
							className="nodrag nopan w-full border-none bg-transparent p-0 text-zinc-100 outline-none placeholder:text-zinc-600"
							placeholder="enter command"
						/>
					</div>
				</div>
			) : null}
			<SideHandles />
		</div>
	);
}

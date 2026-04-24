import { Handle, Position } from "reactflow";

type SideHandlesProps = {
	currentNodeId: string;
	connectionSourceNodeId: string;
};

export function SideHandles({ currentNodeId, connectionSourceNodeId }: SideHandlesProps) {
	const isConnecting = connectionSourceNodeId !== "";
	const isSourceNode = isConnecting && connectionSourceNodeId === currentNodeId;
	const sharedClass =
		"!left-1/2 !right-auto !top-1/2 !transform !h-[16px] !w-[16px] !-translate-x-1/2 !-translate-y-1/2 !rounded-full !border-2 !border-slate-700/50 !bg-white/35";
	const targetClass = isConnecting && !isSourceNode
		? `${sharedClass} !z-20 !cursor-pointer !border-slate-700/70 !bg-white/55`
		: `${sharedClass} !z-10`;
	const sourceClass = isConnecting && !isSourceNode
		? `${sharedClass} !pointer-events-none !z-0`
		: `${sharedClass} !z-20`;

	return (
		<>
			<Handle id="center-target" type="target" position={Position.Top} className={targetClass} />
			<Handle id="center-source" type="source" position={Position.Top} className={sourceClass} isConnectable={!isConnecting || isSourceNode} />
		</>
	);
}

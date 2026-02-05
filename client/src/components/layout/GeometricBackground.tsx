'use client'

export function GeometricBackground() {
  return (
    <div 
      className="fixed inset-0 pointer-events-none select-none overflow-hidden"
      aria-hidden="true"
    >
      {/* Flow column 1 */}
      <div className="absolute right-[220px] top-0">
        <div className="w-1 h-20 bg-gradient-to-b from-transparent via-emerald-400/40 to-transparent rounded-full animate-[flowDown_9s_linear_infinite] [animation-fill-mode:backwards]" />
        <div className="absolute w-6 h-6 border-[1.5px] border-emerald-400/50 animate-[flowDown_12s_linear_infinite_1s] [animation-fill-mode:backwards]" />
        <svg className="absolute w-3 h-3 animate-[flowDown_8s_linear_infinite_5s] [animation-fill-mode:backwards]" viewBox="0 0 12 12">
          <polygon points="6,0 12,12 0,12" fill="rgb(52, 211, 153)" opacity="0.4" />
        </svg>
      </div>

      {/* Flow column 2 */}
      <div className="absolute right-[150px] top-0">
        <div className="w-1 h-16 bg-gradient-to-b from-transparent via-emerald-400/40 to-transparent rounded-full animate-[flowDown_7s_linear_infinite_2s] [animation-fill-mode:backwards]" />
        <svg className="absolute w-5 h-5 animate-[flowDown_10s_linear_infinite] [animation-fill-mode:backwards]" viewBox="0 0 16 16">
          <polygon points="8,0 16,16 0,16" fill="none" stroke="rgb(52, 211, 153)" strokeWidth="1.5" opacity="0.5" />
        </svg>
        <div className="absolute w-3 h-3 bg-emerald-400/30 animate-[flowDown_15s_linear_infinite_4s] [animation-fill-mode:backwards]" />
      </div>

      {/* Flow column 3 */}
      <div className="absolute right-[80px] top-0">
        <div className="w-1 h-[70px] bg-gradient-to-b from-transparent via-emerald-400/40 to-transparent rounded-full animate-[flowDown_11s_linear_infinite_4s] [animation-fill-mode:backwards]" />
        <div className="absolute w-4 h-4 border-[1.5px] border-emerald-400/50 animate-[flowDown_14s_linear_infinite_3s] [animation-fill-mode:backwards]" />
        <svg className="absolute w-4 h-4 animate-[flowDown_18s_linear_infinite_2s] [animation-fill-mode:backwards]" viewBox="0 0 12 12">
          <polygon points="6,0 12,12 0,12" fill="none" stroke="rgb(52, 211, 153)" strokeWidth="1.5" opacity="0.4" />
        </svg>
      </div>

      <style jsx>{`
        @keyframes flowDown {
          0% { transform: translateY(-100px); }
          100% { transform: translateY(100vh); }
        }
      `}</style>
    </div>
  )
}

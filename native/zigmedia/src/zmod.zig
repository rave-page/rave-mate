//! Zig-module surface of zigmedia for native CHILD executables (native/zigenc): no C exports, no libc
//! (root.zig's exports need c_allocator). The child imports this as module "zigmedia" and runs the
//! receiver/sender in-process on its own allocator.
pub const receiver = @import("receiver.zig");
pub const sender = @import("sender.zig");
pub const sock = @import("sock.zig");
pub const hrtimer = @import("hrtimer.zig");
pub const histogram = @import("histogram.zig");
pub const playout = @import("playout.zig");
pub const keys = @import("keys.zig");
pub const wire = @import("wire.zig");

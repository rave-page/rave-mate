const std = @import("std");

// rave-mate-keeper: long-lived, single-instance, pure-D3D11 Spout sender host (no GL, no interop).
// Owns receive destinations so a daemon/media-child restart or self-update never creates a NEW sender
// in Resolume (.devnotes/VRAM_FULL_CARD_POLICY.md 1b). Windows-only.
//   zig build -Drelease      Artifact: zig-out/bin/rave-mate-keeper.exe
pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{ .preferred_optimize_mode = .ReleaseSmall });
    if (target.result.os.tag != .windows) return;

    const mod = b.createModule(.{ .root_source_file = b.path("src/main.zig"), .target = target, .optimize = optimize });
    mod.addImport("d3d11", b.createModule(.{
        .root_source_file = .{ .cwd_relative = b.pathJoin(&.{ b.build_root.path.?, "..", "zigd3d", "src", "d3d11.zig" }) },
        .target = target,
        .optimize = optimize,
    }));
    mod.linkSystemLibrary("d3d11", .{});
    mod.linkSystemLibrary("dxgi", .{});
    const exe = b.addExecutable(.{ .name = "rave-mate-keeper", .root_module = mod });
    exe.subsystem = .Windows; // no console window: started detached by the app
    b.installArtifact(exe);

    const tests = b.addTest(.{ .root_module = mod });
    const test_step = b.step("test", "Run unit tests");
    test_step.dependOn(&b.addRunArtifact(tests).step);
}

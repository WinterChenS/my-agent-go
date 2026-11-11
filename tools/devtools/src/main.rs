//! Binary entry point. All behavior lives in the library so the contract
//! fixtures can call the gate and `verify` directly.

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    std::process::exit(anvil_devtools::run(&args));
}

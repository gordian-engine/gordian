use tonic_build;

fn main() {
    // tonic_build::compile_protos("proto/server/grpc.proto")
    //     .unwrap_or_else(|e| panic!("Failed to compile protos {:?}", e));

    // let current_dir = std::env::current_dir().unwrap();
    // let proto_file = current_dir.parent().unwrap().join("proto/server/grpc.proto");

    // let proto_file = "./proto/server/grpc.proto";
    // tonic_build::configure()
    //     .build_server(false)
    //     .out_dir("./src")
    //     .proto_path("./proto")
    //     .compile(&[proto_file], &["."])
    //     .unwrap_or_else(|e| panic!("protobuf compile error: {}", e));

    tonic_build::configure()
        .build_server(false)
        .build_client(true)
        .out_dir("src") // Specify the output directory for generated Rust files
        .compile(
            &["../proto/server/grpc.proto"], // Paths to your .proto files
            &["../proto"], // Include directories for .proto files
        )
    .unwrap_or_else(|e| panic!("Failed to compile protos {:?}", e));

    println!("cargo build for gcosmos rust grpc done");
}
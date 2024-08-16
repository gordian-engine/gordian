mod server;
use tokio;

#[tokio::main]
async fn main() {
    println!("Clients!");

    let conn = &mut server::gordian_grpc_client::GordianGrpcClient::connect("http://127.0.0.1:9092").await.unwrap();

    let req = server::CurrentBlockRequest{};

    let resp = conn.get_blocks_watermark(req).await.unwrap();
    println!("RESPONSE={:#?}", resp);
}